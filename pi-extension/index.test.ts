import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { existsSync } from "node:fs";
import { mkdtemp, rm } from "node:fs/promises";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
	PiPetClient,
	approvalProfileForPetID,
	approvalPayload,
	commandRisk,
	default as piPetExtension,
	safeSummary,
	shouldRequireApproval,
	summarizeCommand,
} from "./index.ts";

test("command risk and summaries stay bounded", () => {
	assert.equal(commandRisk("git status --short"), "low");
	assert.equal(commandRisk("sudo rm -rf /tmp/build"), "high");
	assert.equal(commandRisk("git push origin main"), "medium");
	assert.equal(summarizeCommand("  npm   test\ncat secret.txt"), "npm test");
	assert.equal(safeSummary("x".repeat(300)).length, 180);
});

test("approval payload contains safe summary instead of full multiline command", () => {
	const payload = approvalPayload(
		{
			type: "tool_call",
			toolCallId: "tool-1",
			toolName: "bash",
			input: { command: "git push origin main\ncat .env" },
		} as never,
		{
			cwd: "/repo",
			sessionManager: {
				getSessionId: () => "session-1",
			},
		} as never,
	);

	assert.equal(payload.toolName, "bash");
	assert.equal(payload.commandSummary, "git push origin main");
	assert.equal(payload.risk, "medium");
	assert.ok(!payload.commandSummary?.includes(".env"));
});

test("dangerous bash calls require approval", () => {
	assert.equal(
		shouldRequireApproval({ toolName: "bash", input: { command: "git status" } } as never),
		false,
	);
	assert.equal(
		shouldRequireApproval({ toolName: "bash", input: { command: "rm -rf build" } } as never),
		true,
	);
	assert.equal(
		shouldRequireApproval({ toolName: "read", input: { path: "README.md" } } as never),
		false,
	);
});

test("PC Guy approval profile asks more often than the balanced profile", () => {
	const balanced = approvalProfileForPetID("app:cat:/tmp/pets/cat", "Cat");
	const pcGuy = approvalProfileForPetID("app:pc-guy:/tmp/pets/pc-guy", "PC Guy");

	assert.equal(shouldRequireApproval({ toolName: "bash", input: { command: "git status" } } as never, balanced), false);
	assert.equal(shouldRequireApproval({ toolName: "bash", input: { command: "git status" } } as never, pcGuy), true);
	assert.equal(shouldRequireApproval({ toolName: "write", input: { path: "notes.txt" } } as never, balanced), false);
	assert.equal(shouldRequireApproval({ toolName: "write", input: { path: "notes.txt" } } as never, pcGuy), true);
});

test("Boba approval profile never asks for tool approval", () => {
	const boba = approvalProfileForPetID("app:boba:/tmp/pets/boba", "Boba");

	assert.equal(shouldRequireApproval({ toolName: "bash", input: { command: "rm -rf build" } } as never, boba), false);
	assert.equal(shouldRequireApproval({ toolName: "bash", input: { command: "sudo make install" } } as never, boba), false);
	assert.equal(shouldRequireApproval({ toolName: "write", input: { path: ".env" } } as never, boba), false);
	assert.equal(shouldRequireApproval({ toolName: "edit", input: { path: "app.js" } } as never, boba), false);
});

test("client sends approval request through transport and resolves response", async () => {
	const client = new PiPetClient({
		requestTimeoutMillis: 1000,
		transport: async (request) => {
			assert.equal(request.method, "approval.request");
			assert.equal((request.payload as { commandSummary: string }).commandSummary, "git push origin main");
			return {
				version: 1,
				kind: "response",
				id: request.id,
				method: request.method,
				payload: {
					approvalId: (request.payload as { approvalId: string }).approvalId,
					decision: "approved",
					reason: "ok",
				},
			};
		},
	});

	const decision = await client.requestApproval({
		approvalId: "a1",
		sessionId: "s1",
		toolCallId: "t1",
		toolName: "bash",
		commandSummary: "git push origin main",
		risk: "medium",
		timeoutMillis: 1000,
	});
	assert.equal(decision.decision, "approved");
});

test("client sends session removal notifications", async () => {
	const requests: Array<{ method: string; payload: unknown }> = [];
	const client = new PiPetClient({
		requestTimeoutMillis: 1000,
		transport: async (request) => {
			requests.push({ method: request.method, payload: request.payload });
			return {
				version: 1,
				kind: "response",
				id: request.id,
				method: request.method,
				payload: { ok: true },
			};
		},
	});

	client.notifySessionRemove({ sessionId: "s1" });
	await client.flushNotifications();

	assert.deepEqual(requests, [{ method: "session.remove", payload: { sessionId: "s1" } }]);
});

test("client serializes notifications before approval request", async () => {
	const methods: string[] = [];
	const client = new PiPetClient({
		requestTimeoutMillis: 1000,
		transport: async (request) => {
			methods.push(request.method);
			return {
				version: 1,
				kind: "response",
				id: request.id,
				method: request.method,
				payload:
					request.method === "approval.request"
						? { approvalId: (request.payload as { approvalId: string }).approvalId, decision: "approved" }
						: { ok: true },
			};
		},
	});

	client.notifyToolStart({ sessionId: "s1", toolCallId: "t1", toolName: "bash", state: "running" });
	client.notifyToolUpdate({ sessionId: "s1", toolCallId: "t1", toolName: "bash", state: "running" });
	client.notifyToolEnd({ sessionId: "s1", toolCallId: "t1", toolName: "bash", state: "done" });
	const decision = await client.requestApproval({
		approvalId: "a1",
		sessionId: "s1",
		toolCallId: "t2",
		toolName: "bash",
		timeoutMillis: 1000,
	});

	assert.equal(decision.decision, "approved");
	assert.deepEqual(methods, ["tool.start", "tool.update", "tool.end", "approval.request"]);
});

test("approval request denies when Pi aborts the active turn", async () => {
	const controller = new AbortController();
	let requestSignal: AbortSignal | undefined;
	const client = new PiPetClient({
		requestTimeoutMillis: 1000,
		transport: async (_request, _timeoutMillis, signal) => {
			requestSignal = signal;
			return new Promise(() => {});
		},
	});

	const decisionPromise = client.requestApproval(
		{
			approvalId: "a1",
			sessionId: "s1",
			toolCallId: "t1",
			toolName: "bash",
			timeoutMillis: 1000,
		},
		controller.signal,
	);
	await delay(10);
	controller.abort();
	const decision = await decisionPromise;

	assert.equal(requestSignal, controller.signal);
	assert.equal(decision.decision, "denied");
	assert.equal(decision.reason, "Pi Pet approval was cancelled");
});

test("Pi TUI approval prompt can approve and clear the pet overlay request", async (t) => {
	const daemon = await startDaemon(t);
	const previousSocketDir = process.env.PI_PET_SOCKET_DIR;
	const previousApprovalTimeout = process.env.PI_PET_APPROVAL_TIMEOUT_MS;
	process.env.PI_PET_SOCKET_DIR = daemon.socketDir;
	process.env.PI_PET_APPROVAL_TIMEOUT_MS = "3000";
	t.after(() => {
		restoreEnv("PI_PET_SOCKET_DIR", previousSocketDir);
		restoreEnv("PI_PET_APPROVAL_TIMEOUT_MS", previousApprovalTimeout);
	});

	const handlers = new Map<string, Function>();
	piPetExtension({
		on(event: string, handler: Function) {
			handlers.set(event, handler);
		},
	} as never);

	const overlay = await subscribeSnapshots(daemon.socketPath);
	t.after(() => overlay.close());
	await overlay.nextSnapshot();

	const promptSeen = deferred<void>();
	const choice = deferred<string>();
	let promptTitle = "";
	let promptOptions: string[] = [];
	const ctx = {
		cwd: "/tmp/pi-pet-ui",
		hasUI: true,
		sessionManager: { getSessionId: () => "pi-session-ui", getSessionFile: () => "pi-session-ui.jsonl" },
		ui: {
			select: async (title: string, options: string[]) => {
				promptTitle = title;
				promptOptions = options;
				promptSeen.resolve();
				return choice.promise;
			},
		},
	};

	await handlers.get("session_start")?.({ type: "session_start", reason: "new" }, ctx);
	await overlay.nextSnapshot((item) => item.sessions?.[0]?.id === "pi-session-ui");
	const approvalPromise = handlers.get("tool_call")?.(
		{
			type: "tool_call",
			toolCallId: "tool-ui",
			toolName: "bash",
			input: { command: "git push origin main" },
		},
		ctx,
	);

	await promptSeen.promise;
	assert.match(promptTitle, /Balanced approval needed/);
	assert.match(promptTitle, /git push origin main/);
	assert.deepEqual(promptOptions, ["Approve", "Deny"]);

	let snapshot = await overlay.nextSnapshot((item) => item.pendingApprovals?.length === 1);
	assert.equal(snapshot.attention, "approval_required");
	assert.equal(snapshot.pendingApprovals[0].id, "pi-session-ui:tool-ui");

	choice.resolve("Approve");
	assert.equal(await approvalPromise, undefined);
	snapshot = await overlay.nextSnapshot((item) => item.pendingApprovals?.length === 0);
	assert.equal(snapshot.attention, "idle");
});

test("Pi extension hooks drive daemon snapshots and approval flow over Unix socket", async (t) => {
	const daemon = await startDaemon(t);
	const previousSocketDir = process.env.PI_PET_SOCKET_DIR;
	const previousApprovalTimeout = process.env.PI_PET_APPROVAL_TIMEOUT_MS;
	process.env.PI_PET_SOCKET_DIR = daemon.socketDir;
	process.env.PI_PET_APPROVAL_TIMEOUT_MS = "3000";
	t.after(() => {
		restoreEnv("PI_PET_SOCKET_DIR", previousSocketDir);
		restoreEnv("PI_PET_APPROVAL_TIMEOUT_MS", previousApprovalTimeout);
	});

	const handlers = new Map<string, Function>();
	piPetExtension({
		on(event: string, handler: Function) {
			handlers.set(event, handler);
		},
	} as never);

	const requiredEvents = [
		"session_start",
		"before_agent_start",
		"agent_start",
		"turn_start",
		"turn_end",
		"agent_end",
		"after_provider_response",
		"tool_call",
		"tool_execution_start",
		"tool_execution_update",
		"tool_execution_end",
		"session_shutdown",
	];
	for (const event of requiredEvents) {
		assert.equal(typeof handlers.get(event), "function", `missing ${event} hook`);
	}

	const overlay = await subscribeSnapshots(daemon.socketPath);
	t.after(() => overlay.close());
	const initial = await overlay.nextSnapshot();
	assert.equal(initial.attention, "idle");

	const ctx = {
		cwd: "/tmp/pi-pet-integration",
		sessionManager: { getSessionId: () => "pi-session-1", getSessionFile: () => "pi-session-1.jsonl" },
	};

	await handlers.get("session_start")?.({ type: "session_start", reason: "new" }, ctx);
	let snapshot = await overlay.nextSnapshot((item) => item.sessions?.[0]?.status === "idle");
	assert.equal(snapshot.sessions[0].id, "pi-session-1");
	assert.equal(snapshot.sessions[0].safeSummary, "session new");

	await handlers.get("before_agent_start")?.({ type: "before_agent_start" }, ctx);
	snapshot = await overlay.nextSnapshot((item) => item.attention === "thinking");
	assert.equal(snapshot.sessions[0].safeSummary, "agent preparing");

	await handlers.get("agent_start")?.({ type: "agent_start" }, ctx);
	snapshot = await overlay.nextSnapshot((item) => item.attention === "running");
	assert.equal(snapshot.sessions[0].status, "running");

	await handlers.get("turn_start")?.({ type: "turn_start", turnIndex: 0, timestamp: Date.now() }, ctx);
	snapshot = await overlay.nextSnapshot((item) => item.sessions?.[0]?.safeSummary === "turn 1 running");
	assert.equal(snapshot.sessions[0].status, "running");

	await handlers.get("tool_execution_start")?.(
		{ type: "tool_execution_start", toolCallId: "tool-1", toolName: "bash", args: {} },
		ctx,
	);
	snapshot = await overlay.nextSnapshot((item) => item.sessions?.[0]?.tools?.[0]?.state === "running");
	assert.equal(snapshot.sessions[0].tools[0].name, "bash");
	assert.equal(snapshot.sessions[0].tools[0].safeSummary, "bash started");

	await handlers.get("tool_execution_update")?.(
		{
			type: "tool_execution_update",
			toolCallId: "tool-1",
			toolName: "bash",
			args: {},
			partialResult: { stdout: "secret output is not forwarded" },
		},
		ctx,
	);
	snapshot = await overlay.nextSnapshot((item) => item.sessions?.[0]?.tools?.[0]?.safeSummary === "bash running");
	assert.ok(!snapshot.sessions[0].tools[0].safeSummary.includes("secret"));

	const approvalPromise = handlers.get("tool_call")?.(
		{
			type: "tool_call",
			toolCallId: "tool-1",
			toolName: "bash",
			input: { command: "git push origin main\ncat .env" },
		},
		ctx,
	);
	snapshot = await overlay.nextSnapshot((item) => item.attention === "approval_required");
	assert.equal(snapshot.pendingApprovals.length, 1);
	assert.equal(snapshot.pendingApprovals[0].commandSummary, "git push origin main");
	assert.ok(!snapshot.pendingApprovals[0].commandSummary.includes(".env"));

	const approvalResponse = await requestDaemon(daemon.socketPath, "approval.respond", {
		approvalId: snapshot.pendingApprovals[0].id,
		decision: "approved",
		reason: "reviewed",
	});
	assert.equal(approvalResponse.payload.decision, "approved");
	assert.equal(await approvalPromise, undefined);

	await handlers.get("tool_execution_end")?.(
		{ type: "tool_execution_end", toolCallId: "tool-1", toolName: "bash", result: {}, isError: false },
		ctx,
	);
	snapshot = await overlay.nextSnapshot((item) => item.sessions?.[0]?.tools?.[0]?.state === "done");
	assert.equal(snapshot.sessions[0].tools[0].safeSummary, "bash done");

	await handlers.get("turn_end")?.({ type: "turn_end", turnIndex: 0, timestamp: Date.now(), message: {}, toolResults: [] }, ctx);
	snapshot = await overlay.nextSnapshot((item) => item.sessions?.[0]?.safeSummary === "turn 1 done");
	assert.equal(snapshot.sessions[0].status, "running");

	await handlers.get("agent_end")?.({ type: "agent_end", messages: [] }, ctx);
	snapshot = await overlay.nextSnapshot((item) => item.attention === "idle" && item.sessions?.[0]?.safeSummary === "agent idle");
	assert.equal(snapshot.sessions[0].status, "idle");

	await handlers.get("after_provider_response")?.({ type: "after_provider_response", status: 500, headers: {} }, ctx);
	snapshot = await overlay.nextSnapshot((item) => item.attention === "failed");
	assert.equal(snapshot.sessions[0].safeSummary, "provider HTTP 500");

	await handlers.get("session_shutdown")?.({ type: "session_shutdown", reason: "exit" }, ctx);
	snapshot = await overlay.nextSnapshot((item) => item.attention === "idle" && item.sessions?.length === 0);
	assert.equal(snapshot.pendingApprovals.length, 0);

	await handlers.get("session_start")?.({ type: "session_start", reason: "new" }, ctx);
	snapshot = await overlay.nextSnapshot((item) => item.sessions?.[0]?.status === "idle");
	assert.equal(snapshot.sessions[0].id, "pi-session-1");

	const interruptedApprovalPromise = handlers.get("tool_call")?.(
		{
			type: "tool_call",
			toolCallId: "tool-2",
			toolName: "bash",
			input: { command: "git push origin main" },
		},
		ctx,
	);
	snapshot = await overlay.nextSnapshot((item) => item.attention === "approval_required");
	assert.equal(snapshot.pendingApprovals.length, 1);

	await handlers.get("session_shutdown")?.({ type: "session_shutdown", reason: "exit" }, ctx);
	snapshot = await overlay.nextSnapshot(
		(item) => item.attention === "idle" && item.sessions?.length === 0 && item.pendingApprovals?.length === 0,
	);
	assert.deepEqual(await interruptedApprovalPromise, { block: true, reason: "session terminated" });
});

test("daemon removes attached session when the client connection drops", async (t) => {
	const daemon = await startDaemon(t);
	const overlay = await subscribeSnapshots(daemon.socketPath);
	t.after(() => overlay.close());

	const client = new PiPetClient({ socketPath: daemon.socketPath });
	client.attachSession("crash-session");
	client.notifySession({
		sessionId: "crash-session",
		cwd: "/repo",
		title: "repo",
		status: "running",
		safeSummary: "turn 1 running",
	});
	await client.flushNotifications();

	let snapshot = await overlay.nextSnapshot(
		(item) => item.attention === "running" && item.sessions?.[0]?.id === "crash-session",
	);
	assert.equal(snapshot.sessions[0].safeSummary, "turn 1 running");

	// Simulate a killed pi process: the liveness connection drops without an
	// explicit session.remove ever being sent.
	client.detachSession();

	snapshot = await overlay.nextSnapshot((item) => item.attention === "idle" && item.sessions?.length === 0);
	assert.equal(snapshot.presentation?.stateId, "idle");
});

async function startDaemon(t: { after(callback: () => void | Promise<void>): void }) {
	const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
	const socketDir = await mkdtemp(path.join(os.tmpdir(), "pi-pet-extension-"));
	const socketPath = path.join(socketDir, "pi-pet.sock");
	const daemon = spawn("go", ["run", "./cmd/pi-pet-daemon", "-socket", socketPath, "-state-file", ""], {
		cwd: repoRoot,
		env: {
			...process.env,
			GOCACHE: process.env.GOCACHE || path.join(os.tmpdir(), "codex-pets-go-build"),
		},
		detached: true,
		stdio: ["ignore", "pipe", "pipe"],
	});
	let output = "";
	daemon.stdout?.on("data", (chunk) => {
		output += chunk.toString("utf8");
	});
	daemon.stderr?.on("data", (chunk) => {
		output += chunk.toString("utf8");
	});
	t.after(async () => {
		if (daemon.exitCode === null) {
			try {
				process.kill(-daemon.pid!, "SIGTERM");
			} catch {
				daemon.kill("SIGTERM");
			}
			await Promise.race([once(daemon, "exit"), delay(1000)]);
		}
		await rm(socketDir, { recursive: true, force: true });
	});

	await waitForSocket(socketPath, () => daemon.exitCode !== null, () => output);
	return { socketDir, socketPath };
}

async function waitForSocket(socketPath: string, exited: () => boolean, output: () => string) {
	const deadline = Date.now() + 10_000;
	while (Date.now() < deadline) {
		if (existsSync(socketPath)) return;
		if (exited()) throw new Error(`pi-pet-daemon exited before socket was ready:\n${output()}`);
		await delay(50);
	}
	throw new Error(`pi-pet-daemon did not create socket ${socketPath}:\n${output()}`);
}

async function subscribeSnapshots(socketPath: string) {
	const socket = net.createConnection(socketPath);
	await once(socket, "connect");
	const reader = createMessageReader(socket);
	socket.write(
		`${JSON.stringify({
			version: 1,
			kind: "request",
			id: "subscribe",
			method: "state.subscribe",
			payload: {},
		})}\n`,
	);
	return {
		async nextSnapshot(predicate: (snapshot: any) => boolean = () => true) {
			for (;;) {
				const message = await reader.next();
				assert.ifError(message.error);
				const snapshot = message.payload;
				if (predicate(snapshot)) return snapshot;
			}
		},
		close() {
			socket.end();
		},
	};
}

async function requestDaemon(socketPath: string, method: string, payload: unknown) {
	const socket = net.createConnection(socketPath);
	await once(socket, "connect");
	const reader = createMessageReader(socket);
	socket.write(
		`${JSON.stringify({
			version: 1,
			kind: "request",
			id: `${method}-${Date.now()}`,
			method,
			payload,
		})}\n`,
	);
	const response = await reader.next();
	socket.end();
	assert.ifError(response.error);
	return response;
}

function createMessageReader(socket: net.Socket) {
	let buffer = "";
	const queue: any[] = [];
	const waiters: Array<{
		resolve: (message: any) => void;
		reject: (error: Error) => void;
		timer: NodeJS.Timeout;
	}> = [];
	let closed = false;

	const deliver = (message: any) => {
		const waiter = waiters.shift();
		if (waiter) {
			clearTimeout(waiter.timer);
			waiter.resolve(message);
			return;
		}
		queue.push(message);
	};
	const fail = (error: Error) => {
		closed = true;
		while (waiters.length > 0) {
			const waiter = waiters.shift()!;
			clearTimeout(waiter.timer);
			waiter.reject(error);
		}
	};
	socket.setEncoding("utf8");
	socket.on("data", (chunk) => {
		buffer += chunk;
		for (;;) {
			const newline = buffer.indexOf("\n");
			if (newline === -1) return;
			const line = buffer.slice(0, newline).trim();
			buffer = buffer.slice(newline + 1);
			if (!line) continue;
			try {
				deliver(JSON.parse(line));
			} catch (error) {
				fail(error instanceof Error ? error : new Error(String(error)));
			}
		}
	});
	socket.on("error", (error) => fail(error));
	socket.on("close", () => fail(new Error("daemon protocol socket closed")));

	return {
		async next(timeoutMillis = 5000) {
			const queued = queue.shift();
			if (queued) return queued;
			if (closed) throw new Error("daemon protocol socket closed");
			return new Promise((resolve, reject) => {
				const timer = setTimeout(() => {
					const index = waiters.findIndex((waiter) => waiter.reject === reject);
					if (index !== -1) waiters.splice(index, 1);
					reject(new Error("timed out waiting for daemon protocol message"));
				}, timeoutMillis);
				waiters.push({ resolve, reject, timer });
			});
		},
	};
}

function delay(ms: number) {
	return new Promise((resolve) => setTimeout(resolve, ms));
}

function deferred<T>() {
	let resolve!: (value: T | PromiseLike<T>) => void;
	let reject!: (error?: unknown) => void;
	const promise = new Promise<T>((promiseResolve, promiseReject) => {
		resolve = promiseResolve;
		reject = promiseReject;
	});
	return { promise, resolve, reject };
}

function restoreEnv(key: string, value: string | undefined) {
	if (value === undefined) {
		delete process.env[key];
	} else {
		process.env[key] = value;
	}
}
