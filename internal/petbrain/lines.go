package petbrain

// newLine mirrors the Swift DialogueEngine.line helper defaults.
func newLine(id string, event MurmurEvent, mood Mood, text string, group string, options ...func(*Line)) Line {
	line := Line{
		ID:                  id,
		Text:                text,
		Triggers:            []string{string(event)},
		Moods:               []string{string(mood)},
		SemanticGroup:       group,
		Rarity:              RarityCommon,
		MinDaysBeforeRepeat: 30,
		CooldownMinutes:     60,
		Tones:               []string{"soft"},
	}
	for _, option := range options {
		option(&line)
	}
	return line
}

func rare() func(*Line)            { return func(l *Line) { l.Rarity = RarityRare } }
func legendary() func(*Line)       { return func(l *Line) { l.Rarity = RarityLegendary } }
func days(value int) func(*Line)   { return func(l *Line) { l.MinDaysBeforeRepeat = value } }
func cool(minutes int) func(*Line) { return func(l *Line) { l.CooldownMinutes = minutes } }
func tones(values ...string) func(*Line) {
	return func(l *Line) { l.Tones = values }
}
func interaction() func(*Line)        { return func(l *Line) { l.RequiresInteraction = true } }
func maxShows(value int) func(*Line)  { return func(l *Line) { l.MaxShowsTotal = value } }

// DefaultLines is the pet's phrase book, ported verbatim from the macOS app.
var DefaultLines = []Line{
	newLine("click_001", EventInteractionClick, MoodHappy, "+1 к уюту.", "interaction_petted", cool(180), interaction()),
	newLine("click_002", EventInteractionClick, MoodHappy, "я официально поглажена.", "interaction_petted", cool(180), interaction()),
	newLine("click_003", EventInteractionClick, MoodHappy, "мрр. но я ничего не говорила.", "interaction_petted", cool(180), interaction()),
	newLine("click_004", EventInteractionClick, MoodHappy, "это было профессиональное поглаживание.", "interaction_petted", cool(180), interaction()),
	newLine("click_005", EventInteractionClick, MoodHappy, "мне кажется, мы продуктивны.", "interaction_productive", cool(240), interaction(), maxShows(4)),
	newLine("click_006", EventInteractionClick, MoodHappy, "ладно, ещё раз можно.", "interaction_petted", cool(180), interaction()),
	newLine("click_007", EventInteractionClick, MoodHappy, "поглаживание принято в backlog.", "interaction_backlog", rare(), cool(420), tones("coding"), interaction()),
	newLine("click_008", EventInteractionClick, MoodHappy, "я вижу, это часть workflow.", "interaction_workflow", rare(), cool(420), tones("coding"), interaction()),

	newLine("spam_001", EventInteractionSpamClick, MoodAnnoyed, "я пиксельная, но чувства настоящие.", "spam_feelings", cool(240), interaction()),
	newLine("spam_002", EventInteractionSpamClick, MoodAnnoyed, "ладно-ладно, я уже милая.", "spam_already_cute", cool(240), interaction()),
	newLine("spam_003", EventInteractionSpamClick, MoodAnnoyed, "пожалуйста, не превращай меня в кнопку.", "spam_not_button", cool(240), interaction()),
	newLine("spam_004", EventInteractionSpamClick, MoodAnnoyed, "я не баг, я фича с ушами.", "spam_feature_ears", rare(), cool(420), tones("coding"), interaction()),

	newLine("near_001", EventMouseNear, MoodCurious, "ты что-то задумал?", "cursor_watch", cool(360)),
	newLine("near_002", EventMouseNear, MoodCurious, "я вижу курсор.", "cursor_watch", cool(360)),
	newLine("near_003", EventMouseNear, MoodCurious, "он приближается.", "cursor_approaches", cool(360)),
	newLine("near_004", EventMouseNear, MoodCurious, "если это drag — я морально готова.", "cursor_drag_ready", cool(420)),
	newLine("near_005", EventMouseNear, MoodCurious, "мы смотрим друг на друга. продуктивно.", "cursor_eye_contact", rare(), cool(720)),

	newLine("drag_001", EventDrag, MoodHappy, "переезд без коробок.", "drag_moved", cool(240), interaction()),
	newLine("drag_002", EventDrag, MoodHappy, "новое место. новая я.", "drag_moved", cool(240), interaction()),
	newLine("drag_003", EventDrag, MoodHappy, "тут лучше. наверное.", "drag_new_place", cool(240), interaction()),
	newLine("drag_004", EventDrag, MoodHappy, "меня поставили. я стою.", "drag_placed", cool(240), interaction()),
	newLine("drag_005", EventDrag, MoodHappy, "географически я изменилась.", "drag_geography", rare(), cool(420), interaction()),
	newLine("drag_006", EventDrag, MoodHappy, "я уже desktop furniture.", "drag_furniture", rare(), cool(720), tones("silly"), interaction()),

	newLine("running_001", EventCodexRunning, MoodFocused, "окей, я пошла копаться в байтах.", "running_start", cool(120), tones("coding")),
	newLine("running_002", EventCodexRunning, MoodFocused, "делаю вид, что всё под контролем.", "running_start", cool(120), tones("coding")),
	newLine("running_003", EventCodexRunning, MoodFocused, "работаю тихо. почти.", "running_start", cool(120), tones("coding")),
	newLine("running_004", EventCodexRunning, MoodFocused, "я в режиме маленького инженера.", "running_tiny_engineer", cool(180), tones("coding")),
	newLine("running_005", EventCodexRunning, MoodFocused, "сейчас что-нибудь придумаем.", "running_start", cool(120), tones("coding")),
	newLine("running_006", EventCodexRunning, MoodFocused, "байты шуршат.", "running_bytes", rare(), cool(360), tones("silly", "coding")),

	newLine("long_running_001", EventCodexLongRunning, MoodFocused, "я всё ещё тут. просто стала философской.", "running_long", cool(360), tones("coding")),
	newLine("long_running_002", EventCodexLongRunning, MoodFocused, "долгая задача. я села рядом.", "running_long", cool(360), tones("soft")),
	newLine("long_running_003", EventCodexLongRunning, MoodFocused, "если что, я охраняю прогресс.", "running_guard", cool(360), tones("soft")),
	newLine("long_running_004", EventCodexLongRunning, MoodFocused, "байты сопротивляются, но мы терпеливые.", "running_bytes_resist", rare(), cool(720), tones("coding")),

	newLine("waiting_001", EventCodexWaiting, MoodWaiting, "кажется, теперь твой ход.", "waiting_user_turn", cool(30), tones("soft", "coding")),
	newLine("waiting_002", EventCodexWaiting, MoodWaiting, "я принесла вопрос.", "waiting_user_turn", cool(30), tones("soft", "coding")),
	newLine("waiting_003", EventCodexWaiting, MoodWaiting, "оно ждёт тебя. я тоже, но милее.", "waiting_user_turn", cool(60), tones("soft")),
	newLine("waiting_004", EventCodexWaiting, MoodWaiting, "там что-то просит внимания.", "waiting_attention", cool(45), tones("coding")),
	newLine("waiting_005", EventCodexWaiting, MoodWaiting, "я не тороплю. просто смотрю.", "waiting_soft", cool(60), tones("soft")),

	newLine("review_001", EventCodexReview, MoodWaiting, "я сложила изменения в аккуратную кучку.", "review_ready", cool(45), tones("coding")),
	newLine("review_002", EventCodexReview, MoodWaiting, "пора посмотреть, что получилось.", "review_ready", cool(45), tones("coding")),
	newLine("review_003", EventCodexReview, MoodWaiting, "я принесла review. оно свежее.", "review_fresh", cool(60), tones("coding")),
	newLine("review_004", EventCodexReview, MoodWaiting, "кажется, это уже можно читать.", "review_ready", cool(45), tones("coding")),
	newLine("review_005", EventCodexReview, MoodWaiting, "готово к человеческому взгляду.", "review_human", cool(60), tones("coding", "soft")),

	newLine("success_001", EventCodexSuccess, MoodHappy, "получилось. я сделала маленький победный круг.", "success_small_victory", cool(45), tones("coding")),
	newLine("success_002", EventCodexSuccess, MoodHappy, "ура. можно моргнуть с гордостью.", "success_proud", cool(60), tones("soft")),
	newLine("success_003", EventCodexSuccess, MoodHappy, "оно зелёное. я довольна.", "success_green", cool(45), tones("coding")),
	newLine("success_004", EventCodexSuccess, MoodHappy, "мы победили одну маленькую неопределённость.", "success_uncertainty", cool(60), tones("coding")),
	newLine("success_005", EventCodexSuccess, MoodHappy, "я знала, что у нас лапки не зря.", "success_paws", rare(), cool(120), tones("soft")),
	newLine("success_006", EventCodexSuccess, MoodHappy, "зелёный день. я одобряю.", "success_green_day", rare(), cool(180), tones("coding")),

	newLine("failed_001", EventCodexFailed, MoodSad, "упс. я аккуратно положила ошибку на стол.", "failed_soft", cool(45), tones("soft", "coding")),
	newLine("failed_002", EventCodexFailed, MoodSad, "что-то хрустнуло. но не мы.", "failed_crunch", cool(45), tones("soft")),
	newLine("failed_003", EventCodexFailed, MoodSad, "оно не прошло. я рядом.", "failed_nearby", cool(45), tones("soft", "coding")),
	newLine("failed_004", EventCodexFailed, MoodSad, "байты сказали ‘нет’, но неубедительно.", "failed_bytes_no", cool(60), tones("coding")),
	newLine("failed_005", EventCodexFailed, MoodSad, "маленький красный флаг. очень маленький.", "failed_red_flag", cool(60), tones("coding")),
	newLine("failed_006", EventCodexFailed, MoodSad, "кажется, баг решил пожить с нами.", "failed_repeat_bug", rare(), cool(180), tones("coding")),
	newLine("failed_007", EventCodexFailed, MoodSad, "я уже принесла плед для stack trace.", "failed_stack_trace_blanket", rare(), cool(240), tones("coding", "soft")),

	newLine("return_001", EventUserReturned, MoodHappy, "о, ты вернулся.", "return_greeting", cool(720), tones("soft")),
	newLine("return_002", EventUserReturned, MoodHappy, "я тут немного поспала.", "return_slept", cool(720), tones("soft")),
	newLine("return_003", EventUserReturned, MoodHappy, "пока тебя не было, я охраняла пиксели.", "return_guarded_pixels", cool(720), tones("soft")),
	newLine("return_004", EventUserReturned, MoodHappy, "добро пожаловать обратно.", "return_greeting", cool(720), tones("soft")),
	newLine("return_005", EventUserReturned, MoodHappy, "я делала вид, что не скучала.", "return_not_missing", rare(), cool(1440), tones("soft")),

	newLine("night_001", EventLateNight, MoodSleepy, "я уже пиксельно зеваю.", "night_sleepy", cool(1440), tones("soft")),
	newLine("night_002", EventLateNight, MoodSleepy, "поздний час. байты тоже хотят спать.", "night_bytes", cool(1440), tones("coding", "soft")),
	newLine("night_003", EventLateNight, MoodSleepy, "я свернулась в маленький if.", "night_if", rare(), cool(1440), tones("coding")),
	newLine("night_004", EventLateNight, MoodSleepy, "ночной режим: мягкие лапки, тихие мысли.", "night_soft", cool(1440), tones("soft")),
	newLine("night_005", EventLateNight, MoodSleepy, "давай ещё чуть-чуть и потом отдыхать. наверное.", "night_soft_nudge", rare(), cool(1440), tones("soft")),

	newLine("ambient_001", EventAmbient, MoodCurious, "я сижу очень ответственно.", "ambient_responsible", cool(1440), tones("soft")),
	newLine("ambient_002", EventAmbient, MoodCurious, "пиксели под контролем.", "ambient_pixels", cool(1440), tones("soft")),
	newLine("ambient_003", EventAmbient, MoodCurious, "у меня маленький план. очень маленький.", "ambient_tiny_plan", cool(1440), tones("soft")),
	newLine("ambient_004", EventAmbient, MoodCurious, "я думала, но тихо.", "ambient_thinking", cool(1440), tones("soft")),
	newLine("ambient_005", EventAmbient, MoodCurious, "всё идёт маленькими шагами.", "ambient_small_steps", cool(1440), tones("soft")),
	newLine("ambient_006", EventAmbient, MoodCurious, "я рядом.", "ambient_nearby", cool(1440), tones("soft")),

	newLine("rare_001", EventAmbient, MoodCurious, "я нашла невидимую крошку. она моя.", "rare_invisible_crumb", rare(), days(45), cool(4320), tones("silly")),
	newLine("rare_002", EventAmbient, MoodCurious, "сегодня я особенно круглая.", "rare_round", rare(), days(45), cool(4320), tones("soft")),
	newLine("rare_003", EventCodexReview, MoodWaiting, "я умею смотреть в сторону проблемы.", "rare_problem_side_eye", rare(), days(45), cool(4320), tones("coding")),
	newLine("rare_004", EventAmbient, MoodCurious, "я положила тревожность в маленькую коробку.", "rare_anxiety_box", rare(), days(45), cool(4320), tones("soft")),
	newLine("rare_005", EventMouseNear, MoodCurious, "кажется, курсор приручён.", "rare_cursor_tamed", rare(), days(45), cool(4320), tones("soft")),
	newLine("egg_001", EventAmbient, MoodCurious, "легенда гласит, что где-то есть идеальный diff.", "egg_perfect_diff", legendary(), days(60), cool(10080), tones("coding")),
	newLine("egg_002", EventCodexRunning, MoodFocused, "я видела TODO. оно видело меня.", "egg_todo", legendary(), days(60), cool(10080), tones("coding")),
	newLine("egg_003", EventAmbient, MoodCurious, "я не отвлекаю. я украшаю периферию.", "egg_periphery", legendary(), days(60), cool(10080), tones("soft")),
}
