import net from "node:net";
import os from "node:os";
import path from "node:path";
import type {
	ExtensionAPI,
	ExtensionContext,
	ToolCallEvent,
	ToolExecutionEndEvent,
	ToolExecutionStartEvent,
	ToolExecutionUpdateEvent,
} from "@ggwplarin/pi-coding-agent";

export const CODEX_PETS_PI_EXTENSION_VERSION = "1.4.0";

const VERSION = 1;

const METHOD_SESSION_UPSERT = "session.upsert";
const METHOD_SESSION_REMOVE = "session.remove";
const METHOD_SESSION_ATTACH = "session.attach";
const METHOD_TOOL_START = "tool.start";
const METHOD_TOOL_UPDATE = "tool.update";
const METHOD_TOOL_END = "tool.end";
const METHOD_APPROVAL_REQUEST = "approval.request";
const METHOD_APPROVAL_RESPOND = "approval.respond";
const METHOD_SNAPSHOT_GET = "snapshot.get";

type SessionStatus = "idle" | "thinking" | "running" | "done" | "failed" | "disconnected";
type ToolState = "running" | "done" | "failed";
type ApprovalDecision = "approved" | "denied" | "expired";

type ProtocolMessage = {
	version: number;
	kind: "request" | "response" | "event";
	id?: string;
	method: string;
	payload?: unknown;
	error?: { code: string; message: string };
};

type SessionPayload = {
	sessionId: string;
	cwd?: string;
	title?: string;
	status: SessionStatus;
	safeSummary?: string;
};

type SessionRemovePayload = {
	sessionId: string;
};

type ToolPayload = {
	sessionId: string;
	toolCallId: string;
	toolName: string;
	state?: ToolState;
	safeSummary?: string;
};

type ApprovalPayload = {
	approvalId: string;
	sessionId: string;
	toolCallId?: string;
	toolName: string;
	commandSummary?: string;
	risk?: string;
	timeoutMillis?: number;
};

type ApprovalResponse = {
	approvalId: string;
	decision: ApprovalDecision;
	reason?: string;
};

type PiPetClientOptions = {
	socketPath?: string;
	requestTimeoutMillis?: number;
	transport?: (message: ProtocolMessage, timeoutMillis: number, signal?: AbortSignal) => Promise<ProtocolMessage>;
};

type MinimalContext = Pick<ExtensionContext, "cwd" | "sessionManager">;

export type PetRef = {
	id?: string;
	displayName?: string;
	source?: string;
	path?: string;
};

export type SnapshotPayload = {
	selectedPetId?: string;
	installedPets?: PetRef[];
};

export type ApprovalProfile = {
	id: string;
	label: string;
	description: string;
	requiresApproval: (event: ToolCallEvent) => boolean;
};

let requestCounter = 0;

export default function piPetExtension(pi: ExtensionAPI): void {
	const client = new PiPetClient();

	pi.on("session_start", (event, ctx) => {
		// Bind the session lifetime to a persistent daemon connection so the
		// daemon cleans up even when this process dies without a shutdown hook.
		client.attachSession(sessionID(ctx));
		client.notifySession(sessionPayload(ctx, "idle", `session ${event.reason}`));
	});

	pi.on("session_shutdown", async (_event, ctx) => {
		client.notifySessionRemove(sessionRemovePayload(ctx));
		// The remove is queued fire-and-forget; without this flush the process
		// exits before the request is written and the pet stays stuck.
		await client.flushNotifications();
		client.detachSession();
	});

	pi.on("before_agent_start", (_event, ctx) => {
		client.notifySession(sessionPayload(ctx, "thinking", "agent preparing"));
	});

	pi.on("agent_start", (_event, ctx) => {
		client.notifySession(sessionPayload(ctx, "running", "agent running"));
	});

	pi.on("turn_start", (event, ctx) => {
		client.notifySession(sessionPayload(ctx, "running", `turn ${event.turnIndex + 1} running`));
	});

	pi.on("turn_end", (event, ctx) => {
		client.notifySession(sessionPayload(ctx, "running", `turn ${event.turnIndex + 1} done`));
	});

	pi.on("agent_end", (_event, ctx) => {
		client.notifySession(sessionPayload(ctx, "idle", "agent idle"));
	});

	pi.on("after_provider_response", (event, ctx) => {
		if (event.status >= 400) {
			client.notifySession(sessionPayload(ctx, "failed", `provider HTTP ${event.status}`));
		}
	});

	pi.on("tool_call", async (event, ctx) => {
		const snapshot = await client.getSnapshot(ctx.signal);
		const profile = approvalProfileForSnapshot(snapshot);
		if (!shouldRequireApproval(event, profile)) return undefined;

		const approval = approvalPayload(event, ctx);
		const decision = await requestApproval(event, ctx, client, approval, profile);
		if (decision.decision !== "approved") {
			return {
				block: true,
				reason: decision.reason || `Pi Pet approval ${decision.decision}`,
			};
		}
		return undefined;
	});

	pi.on("tool_execution_start", (event, ctx) => {
		client.notifyToolStart(toolPayload(event, ctx, "running", "started"));
	});

	pi.on("tool_execution_update", (event, ctx) => {
		client.notifyToolUpdate(toolPayload(event, ctx, "running", "running"));
	});

	pi.on("tool_execution_end", (event, ctx) => {
		client.notifyToolEnd(toolPayload(event, ctx, event.isError ? "failed" : "done", event.isError ? "failed" : "done"));
		if (event.isError) {
			client.notifySession(sessionPayload(ctx, "failed", `${event.toolName} failed`));
		}
	});
}

export class PiPetClient {
	private socketPath: string;
	private requestTimeoutMillis: number;
	private transport: ((message: ProtocolMessage, timeoutMillis: number, signal?: AbortSignal) => Promise<ProtocolMessage>) | undefined;
	private notificationQueue: Promise<void> = Promise.resolve();
	private attachSocket: net.Socket | undefined;
	private attachedSessionId: string | undefined;
	private attachRetryTimer: NodeJS.Timeout | undefined;
	private detached = false;

	constructor(options: PiPetClientOptions = {}) {
		this.socketPath = options.socketPath || defaultSocketPath();
		this.requestTimeoutMillis = options.requestTimeoutMillis ?? 3000;
		this.transport = options.transport;
	}

	/**
	 * Holds a persistent unref'd connection that the daemon uses as a liveness
	 * signal: if this process dies for any reason, the connection drops and
	 * the daemon removes the session itself.
	 */
	attachSession(sessionId: string): void {
		if (this.transport) return;
		this.detached = false;
		if (this.attachedSessionId === sessionId && this.attachSocket) return;
		this.attachedSessionId = sessionId;
		this.attachSocket?.destroy();
		this.attachSocket = undefined;
		this.openAttachConnection();
	}

	detachSession(): void {
		this.detached = true;
		this.attachedSessionId = undefined;
		if (this.attachRetryTimer) {
			clearTimeout(this.attachRetryTimer);
			this.attachRetryTimer = undefined;
		}
		this.attachSocket?.destroy();
		this.attachSocket = undefined;
	}

	private openAttachConnection(): void {
		const sessionId = this.attachedSessionId;
		if (this.detached || !sessionId) return;
		const socket = net.createConnection(this.socketPath);
		// unref keeps this connection from holding the pi process open.
		socket.unref();
		this.attachSocket = socket;
		socket.on("connect", () => {
			const message: ProtocolMessage = {
				version: VERSION,
				kind: "request",
				id: `attach-${++requestCounter}`,
				method: METHOD_SESSION_ATTACH,
				payload: { sessionId },
			};
			socket.write(`${JSON.stringify(message)}\n`);
		});
		socket.on("data", () => {});
		socket.on("error", () => {});
		socket.on("close", () => {
			if (this.attachSocket === socket) {
				this.attachSocket = undefined;
			}
			this.scheduleAttachRetry();
		});
	}

	private scheduleAttachRetry(): void {
		if (this.detached || !this.attachedSessionId || this.attachRetryTimer) return;
		this.attachRetryTimer = setTimeout(() => {
			this.attachRetryTimer = undefined;
			this.openAttachConnection();
		}, 2000);
		this.attachRetryTimer.unref();
	}

	notifySession(payload: SessionPayload): void {
		this.enqueueNotification(METHOD_SESSION_UPSERT, payload);
	}

	notifySessionRemove(payload: SessionRemovePayload): void {
		this.enqueueNotification(METHOD_SESSION_REMOVE, payload);
	}

	notifyToolStart(payload: ToolPayload): void {
		this.enqueueNotification(METHOD_TOOL_START, payload);
	}

	notifyToolUpdate(payload: ToolPayload): void {
		this.enqueueNotification(METHOD_TOOL_UPDATE, payload);
	}

	notifyToolEnd(payload: ToolPayload): void {
		this.enqueueNotification(METHOD_TOOL_END, payload);
	}

	async requestApproval(payload: ApprovalPayload, signal?: AbortSignal): Promise<ApprovalResponse> {
		try {
			await withAbort(this.flushNotifications(), signal);
			const response = await this.request(METHOD_APPROVAL_REQUEST, payload, payload.timeoutMillis || 10 * 60 * 1000, signal);
			return response.payload as ApprovalResponse;
		} catch (error) {
			return {
				approvalId: payload.approvalId,
				decision: "denied",
				reason: isAbortError(error) ? "Pi Pet approval was cancelled" : "Pi Pet daemon is unavailable for approval",
			};
		}
	}

	async respondToApproval(payload: ApprovalResponse, signal?: AbortSignal): Promise<void> {
		try {
			await this.request(METHOD_APPROVAL_RESPOND, payload, this.requestTimeoutMillis, signal);
		} catch {
			// The overlay may have already resolved or the daemon may be unavailable.
		}
	}

	async getSnapshot(signal?: AbortSignal): Promise<SnapshotPayload | undefined> {
		try {
			const response = await this.request(METHOD_SNAPSHOT_GET, {}, this.requestTimeoutMillis, signal);
			return response.payload as SnapshotPayload;
		} catch {
			return undefined;
		}
	}

	async flushNotifications(): Promise<void> {
		await this.notificationQueue;
	}

	async request(
		method: string,
		payload: unknown,
		timeoutMillis = this.requestTimeoutMillis,
		signal?: AbortSignal,
	): Promise<ProtocolMessage> {
		const id = `${Date.now().toString(36)}-${++requestCounter}`;
		const message: ProtocolMessage = {
			version: VERSION,
			kind: "request",
			id,
			method,
			payload,
		};
		if (signal?.aborted) throw abortError();
		if (this.transport) {
			return withAbort(this.transport(message, timeoutMillis, signal), signal);
		}
		const line = `${JSON.stringify(message)}\n`;

		return new Promise((resolve, reject) => {
			const socket = net.createConnection(this.socketPath);
			let buffer = "";
			let settled = false;
			const cleanup = () => {
				clearTimeout(timer);
				signal?.removeEventListener("abort", onAbort);
			};
			const fail = (error: Error) => {
				if (settled) return;
				settled = true;
				cleanup();
				socket.destroy();
				reject(error);
			};
			const succeed = (response: ProtocolMessage) => {
				if (settled) return;
				settled = true;
				cleanup();
				socket.end();
				resolve(response);
			};
			const onAbort = () => fail(abortError());
			const timer = setTimeout(() => {
				fail(new Error("pi pet daemon request timed out"));
			}, timeoutMillis);
			signal?.addEventListener("abort", onAbort, { once: true });

			socket.on("connect", () => {
				socket.write(line);
			});

			socket.on("data", (chunk) => {
				buffer += chunk.toString("utf8");
				const newline = buffer.indexOf("\n");
				if (newline === -1) return;
				const raw = buffer.slice(0, newline);
				try {
					const response = JSON.parse(raw) as ProtocolMessage;
					if (response.error) {
						fail(new Error(response.error.message));
					} else {
						succeed(response);
					}
				} catch (error) {
					fail(error instanceof Error ? error : new Error(String(error)));
				}
			});

			socket.on("error", (error) => {
				fail(error);
			});

			socket.on("close", () => {
				cleanup();
			});
		});
	}

	private enqueueNotification(method: string, payload: unknown): void {
		this.notificationQueue = this.notificationQueue.then(
			() => this.request(method, payload).then(
				() => {},
				() => {},
			),
			() => {},
		);
	}
}

export function defaultSocketPath(): string {
	const runtimeDir = process.env.PI_PET_SOCKET_DIR || process.env.XDG_RUNTIME_DIR;
	if (runtimeDir) return path.join(runtimeDir, "pi-pet.sock");
	return path.join(os.tmpdir(), `codex-pets-${os.userInfo().uid}`, "pi-pet.sock");
}

export function shouldRequireApproval(event: ToolCallEvent, profile: ApprovalProfile = BALANCED_APPROVAL_PROFILE): boolean {
	if (process.env.PI_PET_APPROVE_ALL_TOOLS === "1") return true;
	return profile.requiresApproval(event);
}

export function approvalProfileForSnapshot(snapshot?: SnapshotPayload): ApprovalProfile {
	const selected = selectedPet(snapshot);
	return approvalProfileForPetID(selected?.id ?? snapshot?.selectedPetId, selected?.displayName);
}

export function approvalProfileForPetID(petID?: string, displayName?: string): ApprovalProfile {
	const slug = slugFromPetID(petID) || slugifyPetName(displayName);
	if (slug === "boba") return BOBA_APPROVAL_PROFILE;
	if (slug === "pc-guy") return PC_GUY_APPROVAL_PROFILE;
	return BALANCED_APPROVAL_PROFILE;
}

function selectedPet(snapshot?: SnapshotPayload): PetRef | undefined {
	const selectedPetId = snapshot?.selectedPetId;
	if (!selectedPetId) return undefined;
	return snapshot?.installedPets?.find((pet) => pet.id === selectedPetId) ?? { id: selectedPetId };
}

const BALANCED_APPROVAL_PROFILE: ApprovalProfile = {
	id: "balanced",
	label: "Balanced",
	description: "asks before medium or high risk shell commands",
	requiresApproval(event) {
		return balancedRequiresApproval(event);
	},
};

const PC_GUY_APPROVAL_PROFILE: ApprovalProfile = {
	id: "pc-guy",
	label: "PC Guy",
	description: "asks before every shell command and file edit",
	requiresApproval(event) {
		if (event.toolName === "edit" || event.toolName === "write") return true;
		if (event.toolName === "bash") return true;
		return balancedRequiresApproval(event);
	},
};

const BOBA_APPROVAL_PROFILE: ApprovalProfile = {
	id: "boba",
	label: "Boba",
	description: "never asks for Pi tool approval",
	requiresApproval() {
		return false;
	},
};

function balancedRequiresApproval(event: ToolCallEvent): boolean {
	if (event.toolName !== "bash") return false;
	const command = String((event.input as { command?: unknown }).command || "");
	return commandRisk(command) !== "low";
}

function slugFromPetID(petID?: string): string | undefined {
	if (!petID) return undefined;
	const parts = petID.split(":");
	if (parts.length >= 2 && parts[1]) return slugifyPetName(parts[1]);
	return slugifyPetName(petID);
}

function slugifyPetName(value?: string): string | undefined {
	const slug = value
		?.toLowerCase()
		.normalize("NFKD")
		.replace(/[\u0300-\u036f]/g, "")
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-+|-+$/g, "");
	return slug || undefined;
}

export function commandRisk(command: string): "low" | "medium" | "high" {
	const normalized = command.toLowerCase();
	if (/\brm\s+(-rf|-fr|--recursive)\b/.test(normalized)) return "high";
	if (/\bsudo\b/.test(normalized)) return "high";
	if (/\bgit\s+push\b/.test(normalized)) return "medium";
	if (/\bchmod\b.*\b777\b/.test(normalized)) return "medium";
	if (/\bchown\b/.test(normalized)) return "medium";
	return "low";
}

export function summarizeToolCall(event: Pick<ToolCallEvent, "toolName" | "input">): string {
	if (event.toolName === "bash") {
		return summarizeCommand(String((event.input as { command?: unknown }).command || ""));
	}
	if (event.toolName === "read") {
		return safeSummary(`read ${(event.input as { path?: unknown }).path || ""}`);
	}
	if (event.toolName === "edit" || event.toolName === "write") {
		return safeSummary(`${event.toolName} ${(event.input as { path?: unknown }).path || ""}`);
	}
	return safeSummary(event.toolName);
}

export function summarizeCommand(command: string): string {
	const firstLine = command.split(/\r?\n/, 1)[0] || "";
	const collapsed = firstLine.replace(/\s+/g, " ").trim();
	return safeSummary(collapsed);
}

export function safeSummary(value: string): string {
	const collapsed = value.replace(/\s+/g, " ").trim();
	return collapsed.length <= 180 ? collapsed : collapsed.slice(0, 180);
}

export function sessionPayload(ctx: MinimalContext, status: SessionStatus, safeSummary?: string): SessionPayload {
	return {
		sessionId: sessionID(ctx),
		cwd: ctx.cwd || process.cwd(),
		title: sessionTitle(ctx),
		status,
		safeSummary: safeSummary ? safeSummary : undefined,
	};
}

export function sessionRemovePayload(ctx: MinimalContext): SessionRemovePayload {
	return {
		sessionId: sessionID(ctx),
	};
}

export function approvalPayload(event: ToolCallEvent, ctx: MinimalContext): ApprovalPayload {
	const sessionId = sessionID(ctx);
	const risk = event.toolName === "bash" ? commandRisk(String((event.input as { command?: unknown }).command || "")) : "medium";
	return {
		approvalId: `${sessionId}:${event.toolCallId}`,
		sessionId,
		toolCallId: event.toolCallId,
		toolName: event.toolName,
		commandSummary: summarizeToolCall(event),
		risk,
		timeoutMillis: positiveInt(process.env.PI_PET_APPROVAL_TIMEOUT_MS) || 10 * 60 * 1000,
	};
}

async function requestApproval(
	event: ToolCallEvent,
	ctx: ExtensionContext,
	client: PiPetClient,
	approval: ApprovalPayload,
	profile: ApprovalProfile,
): Promise<ApprovalResponse> {
	if (!ctx.hasUI) {
		return client.requestApproval(approval, ctx.signal);
	}

	const uiController = new AbortController();
	const unlinkAbort = linkAbortSignals(ctx.signal, uiController);
	const daemonDecision = client.requestApproval(approval, ctx.signal).then((decision): Promise<ApprovalOutcome> | ApprovalOutcome => {
		if (isDaemonUnavailableDecision(decision)) return neverSettledApproval();
		return { source: "daemon", decision };
	});
	const uiDecision = promptForApproval(event, ctx, approval, profile, uiController.signal).then((decision) => ({
		source: "tui" as const,
		decision,
	}));

	try {
		const winner = await Promise.race([daemonDecision, uiDecision]);
		if (winner.source === "daemon") {
			uiController.abort();
		} else {
			await client.respondToApproval(winner.decision, ctx.signal);
		}
		return winner.decision;
	} finally {
		unlinkAbort();
	}
}

type ApprovalOutcome = {
	source: "daemon" | "tui";
	decision: ApprovalResponse;
};

async function promptForApproval(
	event: ToolCallEvent,
	ctx: ExtensionContext,
	approval: ApprovalPayload,
	profile: ApprovalProfile,
	signal: AbortSignal,
): Promise<ApprovalResponse> {
	try {
		const choice = await ctx.ui.select(approvalPromptTitle(event, approval, profile), ["Approve", "Deny"], {
			signal,
			timeout: approval.timeoutMillis,
		});
		if (choice === "Approve") {
			return {
				approvalId: approval.approvalId,
				decision: "approved",
				reason: "Approved in Pi TUI",
			};
		}
		return {
			approvalId: approval.approvalId,
			decision: "denied",
			reason: choice === "Deny" ? "Denied in Pi TUI" : "Pi TUI approval was dismissed",
		};
	} catch (error) {
		return {
			approvalId: approval.approvalId,
			decision: "denied",
			reason: isAbortError(error) ? "Pi Pet approval was cancelled" : "Pi TUI approval failed",
		};
	}
}

function approvalPromptTitle(event: ToolCallEvent, approval: ApprovalPayload, profile: ApprovalProfile): string {
	const summary = approval.commandSummary || summarizeToolCall(event);
	const lines = [
		`${profile.label} approval needed`,
		`${event.toolName}: ${summary || event.toolName}`,
		`Rule: ${profile.description}`,
	];
	if (approval.risk) lines.push(`Risk: ${approval.risk}`);
	return lines.join("\n");
}

function isDaemonUnavailableDecision(decision: ApprovalResponse): boolean {
	return decision.decision === "denied" && decision.reason === "Pi Pet daemon is unavailable for approval";
}

function neverSettledApproval(): Promise<ApprovalOutcome> {
	return new Promise(() => {});
}

function linkAbortSignals(parent: AbortSignal | undefined, controller: AbortController): () => void {
	if (!parent) return () => {};
	if (parent.aborted) {
		controller.abort();
		return () => {};
	}
	const onAbort = () => controller.abort();
	parent.addEventListener("abort", onAbort, { once: true });
	return () => parent.removeEventListener("abort", onAbort);
}

function toolPayload(
	event: ToolExecutionStartEvent | ToolExecutionUpdateEvent | ToolExecutionEndEvent,
	ctx: MinimalContext,
	state: ToolState,
	verb: string,
): ToolPayload {
	return {
		sessionId: sessionID(ctx),
		toolCallId: event.toolCallId,
		toolName: event.toolName,
		state,
		safeSummary: safeSummary(`${event.toolName} ${verb}`),
	};
}

function withAbort<T>(promise: Promise<T>, signal?: AbortSignal): Promise<T> {
	if (!signal) return promise;
	if (signal.aborted) return Promise.reject(abortError());
	return new Promise((resolve, reject) => {
		const onAbort = () => reject(abortError());
		signal.addEventListener("abort", onAbort, { once: true });
		promise.then(resolve, reject).finally(() => {
			signal.removeEventListener("abort", onAbort);
		});
	});
}

function abortError(): Error {
	const error = new Error("pi pet request aborted");
	error.name = "AbortError";
	return error;
}

function isAbortError(error: unknown): boolean {
	return error instanceof Error && error.name === "AbortError";
}

function sessionID(ctx: MinimalContext): string {
	const manager = ctx.sessionManager as unknown as {
		getSessionId?: () => string;
		getSessionFile?: () => string | undefined;
	};
	return manager.getSessionId?.() || manager.getSessionFile?.() || `${ctx.cwd || process.cwd()}:${process.pid}`;
}

function sessionTitle(ctx: MinimalContext): string {
	const cwd = ctx.cwd || process.cwd();
	return path.basename(cwd) || cwd;
}

function positiveInt(value: string | undefined): number | undefined {
	if (!value) return undefined;
	const parsed = Number.parseInt(value, 10);
	return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined;
}
