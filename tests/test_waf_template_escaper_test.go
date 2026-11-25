package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-fix-a-17"
)

func t5Limits() impl.Limits {
	return impl.Limits{MaxRules: 8, MaxTemplateBytes: 128, MaxRenderedBytes: 256, MaxSlots: 8}
}

func t5Compile(t *testing.T, e *impl.WafTemplateEscaper, tpl impl.Template) impl.CompiledRule {
	t.Helper()
	rule, err := e.Compile(tpl)
	if err != nil {
		t.Fatalf("compile %q must succeed, got %v", tpl.ID, err)
	}
	return rule
}

func t5LiteralTemplate(id string) impl.Template {
	return impl.Template{
		ID:     id,
		Source: `uri="%{path}"`,
		Slots:  []impl.Slot{{Name: "path", Class: impl.SlotLiteral}},
	}
}

func TestCompileRejectsBlankDuplicateAndOverLimit(t *testing.T) {
	e := impl.NewWafTemplateEscaper(impl.Limits{MaxRules: 1, MaxTemplateBytes: 128, MaxRenderedBytes: 256, MaxSlots: 8})
	if _, err := e.Compile(impl.Template{ID: "  ", Source: `x="%{p}"`, Slots: []impl.Slot{{Name: "p", Class: impl.SlotLiteral}}}); !errors.Is(err, impl.ErrBlankRuleID) {
		t.Fatalf("blank rule id must report ErrBlankRuleID, got %v", err)
	}
	if _, err := e.Compile(impl.Template{ID: "r0", Source: "", Slots: nil}); !errors.Is(err, impl.ErrBadTemplate) {
		t.Fatalf("empty source must report ErrBadTemplate, got %v", err)
	}
	t5Compile(t, e, t5LiteralTemplate("r1"))
	if _, err := e.Compile(t5LiteralTemplate("r1")); !errors.Is(err, impl.ErrDuplicateRule) {
		t.Fatalf("duplicate rule id must report ErrDuplicateRule, got %v", err)
	}
	if _, err := e.Compile(t5LiteralTemplate("r2")); !errors.Is(err, impl.ErrLimitExceeded) {
		t.Fatalf("second rule over cap must report ErrLimitExceeded, got %v", err)
	}
}

func TestCompileRejectsBadSlotDeclarations(t *testing.T) {
	e := impl.NewWafTemplateEscaper(t5Limits())
	if _, err := e.Compile(impl.Template{ID: "r1", Source: `a="%{path}"`, Slots: nil}); !errors.Is(err, impl.ErrUndeclaredSlot) {
		t.Fatalf("placeholder without declaration must report ErrUndeclaredSlot, got %v", err)
	}
	if _, err := e.Compile(impl.Template{ID: "r1", Source: `a="%{path}"`, Slots: []impl.Slot{
		{Name: "path", Class: impl.SlotLiteral},
		{Name: "path", Class: impl.SlotLiteral},
	}}); !errors.Is(err, impl.ErrDuplicateSlot) {
		t.Fatalf("duplicate slot names must report ErrDuplicateSlot, got %v", err)
	}
	if _, err := e.Compile(impl.Template{ID: "r1", Source: `a="%{path}"`, Slots: []impl.Slot{
		{Name: "path", Class: impl.SlotClass("glob")},
	}}); !errors.Is(err, impl.ErrBadSlotClass) {
		t.Fatalf("unknown slot class must report ErrBadSlotClass, got %v", err)
	}
	if _, err := e.Compile(impl.Template{ID: "r1", Source: `a="%{path}"`, Slots: []impl.Slot{
		{Name: "path", Class: impl.SlotLiteral},
		{Name: "extra", Class: impl.SlotLiteral},
	}}); !errors.Is(err, impl.ErrUnusedSlot) {
		t.Fatalf("declared but unused slot must report ErrUnusedSlot, got %v", err)
	}
}

func TestCompileRejectsUnbalancedSource(t *testing.T) {
	e := impl.NewWafTemplateEscaper(t5Limits())
	slot := []impl.Slot{{Name: "path", Class: impl.SlotLiteral}}
	for _, source := range []string{`uri="%{path}`, `(%{path}`, `x=%{path}%`} {
		if _, err := e.Compile(impl.Template{ID: "r1", Source: source, Slots: slot}); !errors.Is(err, impl.ErrBadTemplate) {
			t.Fatalf("source %q must report ErrBadTemplate, got %v", source, err)
		}
	}
	if _, ok := e.Rule("r1"); ok {
		t.Fatalf("rejected templates must not be registered")
	}
}

func TestCompiledRuleIsInactiveUntilActivated(t *testing.T) {
	e := impl.NewWafTemplateEscaper(t5Limits())
	rule := t5Compile(t, e, t5LiteralTemplate("r1"))
	if rule.Active {
		t.Fatalf("freshly compiled rule must be inactive")
	}
	if rule.Digest == "" || rule.Generation != 1 || rule.Source != `uri="%{path}"` {
		t.Fatalf("compiled rule metadata = %+v, want digest, generation 1 and the source", rule)
	}
	stored, ok := e.Rule("r1")
	if !ok || stored.Active {
		t.Fatalf("rule lookup = %+v ok=%v, want an inactive rule", stored, ok)
	}
	if _, err := e.Render("r1", map[string]string{"path": "x"}); !errors.Is(err, impl.ErrRuleNotActive) {
		t.Fatalf("render before activation must report ErrRuleNotActive, got %v", err)
	}
	if err := e.Activate("r1"); err != nil {
		t.Fatalf("activate must succeed, got %v", err)
	}
	if stored, _ := e.Rule("r1"); !stored.Active {
		t.Fatalf("rule must be active after activation")
	}
	if err := e.Activate("r1"); !errors.Is(err, impl.ErrAlreadyActive) {
		t.Fatalf("second activation must report ErrAlreadyActive, got %v", err)
	}
	if err := e.Deactivate("r1"); err != nil {
		t.Fatalf("deactivate must succeed, got %v", err)
	}
	if _, err := e.Render("r1", map[string]string{"path": "x"}); !errors.Is(err, impl.ErrRuleNotActive) {
		t.Fatalf("render after deactivation must report ErrRuleNotActive, got %v", err)
	}
	if err := e.Deactivate("r1"); !errors.Is(err, impl.ErrNotActive) {
		t.Fatalf("second deactivation must report ErrNotActive, got %v", err)
	}
}

func TestRenderLiteralEscaping(t *testing.T) {
	e := impl.NewWafTemplateEscaper(t5Limits())
	t5Compile(t, e, t5LiteralTemplate("r1"))
	if err := e.Activate("r1"); err != nil {
		t.Fatalf("activate must succeed, got %v", err)
	}
	got, err := e.Render("r1", map[string]string{"path": "a\"b\\c"})
	if err != nil {
		t.Fatalf("render must succeed, got %v", err)
	}
	want := "uri=\"a\\\"b\\\\c\""
	if got != want {
		t.Fatalf("rendered = %q, want %q (quote and backslash escaped)", got, want)
	}
}

func TestRenderRegexEscaping(t *testing.T) {
	e := impl.NewWafTemplateEscaper(t5Limits())
	tpl := impl.Template{
		ID:     "r1",
		Source: `re="%{pat}"`,
		Slots:  []impl.Slot{{Name: "pat", Class: impl.SlotRegex}},
	}
	t5Compile(t, e, tpl)
	if err := e.Activate("r1"); err != nil {
		t.Fatalf("activate must succeed, got %v", err)
	}
	got, err := e.Render("r1", map[string]string{"pat": "a.b(c)*"})
	if err != nil {
		t.Fatalf("render must succeed, got %v", err)
	}
	want := "re=\"a\\.b\\(c\\)\\*\""
	if got != want {
		t.Fatalf("rendered = %q, want %q (regex metacharacters escaped)", got, want)
	}
}

func TestRenderHeaderNormalization(t *testing.T) {
	e := impl.NewWafTemplateEscaper(t5Limits())
	tpl := impl.Template{
		ID:     "r1",
		Source: `h="%{hdr}"`,
		Slots:  []impl.Slot{{Name: "hdr", Class: impl.SlotHeader}},
	}
	t5Compile(t, e, tpl)
	if err := e.Activate("r1"); err != nil {
		t.Fatalf("activate must succeed, got %v", err)
	}
	got, err := e.Render("r1", map[string]string{"hdr": "X-Forwarded-For"})
	if err != nil {
		t.Fatalf("render must succeed, got %v", err)
	}
	if got != "h=\"x-forwarded-for\"" {
		t.Fatalf("rendered = %q, want the lowercased header name", got)
	}
	for _, bad := range []string{"x y", "x\r\nInjected: 1", ""} {
		if _, err := e.Render("r1", map[string]string{"hdr": bad}); !errors.Is(err, impl.ErrInjectionBlocked) {
			t.Fatalf("header value %q must report ErrInjectionBlocked, got %v", bad, err)
		}
	}
}

func TestRenderNumberNormalization(t *testing.T) {
	e := impl.NewWafTemplateEscaper(t5Limits())
	tpl := impl.Template{
		ID:     "r1",
		Source: `n=%{num}`,
		Slots:  []impl.Slot{{Name: "num", Class: impl.SlotNumber}},
	}
	t5Compile(t, e, tpl)
	if err := e.Activate("r1"); err != nil {
		t.Fatalf("activate must succeed, got %v", err)
	}
	got, err := e.Render("r1", map[string]string{"num": "00042"})
	if err != nil {
		t.Fatalf("render must succeed, got %v", err)
	}
	if got != "n=42" {
		t.Fatalf("rendered = %q, want canonical decimal 42", got)
	}
	for _, bad := range []string{"4x", "-1", ""} {
		if _, err := e.Render("r1", map[string]string{"num": bad}); !errors.Is(err, impl.ErrInjectionBlocked) {
			t.Fatalf("number value %q must report ErrInjectionBlocked, got %v", bad, err)
		}
	}
}

func TestRenderMissingValue(t *testing.T) {
	e := impl.NewWafTemplateEscaper(t5Limits())
	tpl := impl.Template{
		ID:     "r1",
		Source: `p="%{path}" n=%{num}`,
		Slots: []impl.Slot{
			{Name: "path", Class: impl.SlotLiteral},
			{Name: "num", Class: impl.SlotNumber},
		},
	}
	t5Compile(t, e, tpl)
	if err := e.Activate("r1"); err != nil {
		t.Fatalf("activate must succeed, got %v", err)
	}
	if _, err := e.Render("r1", map[string]string{"path": "a"}); !errors.Is(err, impl.ErrMissingValue) {
		t.Fatalf("missing slot value must report ErrMissingValue, got %v", err)
	}
	got, err := e.Render("r1", map[string]string{"path": "a", "num": "7"})
	if err != nil {
		t.Fatalf("render with all values must succeed, got %v", err)
	}
	if got != "p=\"a\" n=7" {
		t.Fatalf("rendered = %q, want p=\"a\" n=7", got)
	}
}

func TestRenderBlocksControlCharacters(t *testing.T) {
	e := impl.NewWafTemplateEscaper(t5Limits())
	literal := impl.Template{ID: "r1", Source: `x="%{v}"`, Slots: []impl.Slot{{Name: "v", Class: impl.SlotLiteral}}}
	regex := impl.Template{ID: "r2", Source: `y="%{v}"`, Slots: []impl.Slot{{Name: "v", Class: impl.SlotRegex}}}
	t5Compile(t, e, literal)
	t5Compile(t, e, regex)
	for _, id := range []string{"r1", "r2"} {
		if err := e.Activate(id); err != nil {
			t.Fatalf("activate %q must succeed, got %v", id, err)
		}
	}
	for _, value := range []string{"a\nb", "a\x00b"} {
		if _, err := e.Render("r1", map[string]string{"v": value}); !errors.Is(err, impl.ErrInjectionBlocked) {
			t.Fatalf("literal value %q must report ErrInjectionBlocked, got %v", value, err)
		}
	}
	if _, err := e.Render("r2", map[string]string{"v": "a\tb"}); !errors.Is(err, impl.ErrInjectionBlocked) {
		t.Fatalf("regex value with a tab must report ErrInjectionBlocked, got %v", err)
	}
}

func TestCanonicalizeSingleLayer(t *testing.T) {
	e := impl.NewWafTemplateEscaper(t5Limits())
	once, err := e.Canonicalize("%2541")
	if err != nil {
		t.Fatalf("canonicalize must succeed, got %v", err)
	}
	if once.Value != "%41" || once.Layers != 1 || !once.Suspicious {
		t.Fatalf("canonicalize %%2541 = %+v, want one layer decoded to %%41 marked suspicious", once)
	}
	plain, err := e.Canonicalize("%41")
	if err != nil {
		t.Fatalf("canonicalize must succeed, got %v", err)
	}
	if plain.Value != "A" || plain.Layers != 1 || plain.Suspicious {
		t.Fatalf("canonicalize %%41 = %+v, want A with one layer and no suspicion", plain)
	}
	raw, err := e.Canonicalize("plain")
	if err != nil {
		t.Fatalf("canonicalize must succeed, got %v", err)
	}
	if raw.Value != "plain" || raw.Layers != 0 || raw.Suspicious {
		t.Fatalf("canonicalize plain = %+v, want the input untouched with zero layers", raw)
	}
	if _, err := e.Canonicalize("%zz"); !errors.Is(err, impl.ErrBadEscape) {
		t.Fatalf("malformed escape must report ErrBadEscape, got %v", err)
	}
}

func TestRenderEnforcesLengthBudget(t *testing.T) {
	e := impl.NewWafTemplateEscaper(impl.Limits{MaxRules: 4, MaxTemplateBytes: 64, MaxRenderedBytes: 8, MaxSlots: 4})
	t5Compile(t, e, t5LiteralTemplate("r1"))
	if err := e.Activate("r1"); err != nil {
		t.Fatalf("activate must succeed, got %v", err)
	}
	short, err := e.Render("r1", map[string]string{"path": "hi"})
	if err != nil {
		t.Fatalf("short render must succeed, got %v", err)
	}
	if short != "uri=\"hi\"" {
		t.Fatalf("short render = %q, want uri=\"hi\"", short)
	}
	if _, err := e.Render("r1", map[string]string{"path": "hello!!!"}); !errors.Is(err, impl.ErrRenderTooLong) {
		t.Fatalf("render beyond the byte budget must report ErrRenderTooLong, got %v", err)
	}
}

func TestConcurrentRenderIsStable(t *testing.T) {
	e := impl.NewWafTemplateEscaper(t5Limits())
	t5Compile(t, e, t5LiteralTemplate("r1"))
	if err := e.Activate("r1"); err != nil {
		t.Fatalf("activate must succeed, got %v", err)
	}
	const workers = 20
	want := "uri=\"abc\\\"def\""
	var wg sync.WaitGroup
	errCh := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := e.Render("r1", map[string]string{"path": "abc\"def"})
			if err != nil {
				errCh <- err.Error()
				return
			}
			if got != want {
				errCh <- got
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Fatalf("concurrent render diverged: %q", msg)
	}
}

func TestPreviewDoesNotRegisterRule(t *testing.T) {
	e := impl.NewWafTemplateEscaper(t5Limits())
	tpl := impl.Template{ID: "tmp", Source: `x="%{p}"`, Slots: []impl.Slot{{Name: "p", Class: impl.SlotLiteral}}}
	got, err := e.Preview(tpl, map[string]string{"p": "a\"b"})
	if err != nil {
		t.Fatalf("preview must succeed, got %v", err)
	}
	if got != "x=\"a\\\"b\"" {
		t.Fatalf("preview rendered = %q, want escaped preview", got)
	}
	if _, ok := e.Rule("tmp"); ok {
		t.Fatalf("preview must not register the template")
	}
	if _, err := e.Render("tmp", map[string]string{"p": "a"}); !errors.Is(err, impl.ErrUnknownRule) {
		t.Fatalf("render of a previewed template must report ErrUnknownRule, got %v", err)
	}
}

func TestSnapshotRestorePreservesRulesAndFlags(t *testing.T) {
	e := impl.NewWafTemplateEscaper(t5Limits())
	t5Compile(t, e, t5LiteralTemplate("r1"))
	number := impl.Template{ID: "r2", Source: `n=%{num}`, Slots: []impl.Slot{{Name: "num", Class: impl.SlotNumber}}}
	t5Compile(t, e, number)
	if err := e.Activate("r1"); err != nil {
		t.Fatalf("activate r1 must succeed, got %v", err)
	}
	data, err := e.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	if err := e.Deactivate("r1"); err != nil {
		t.Fatalf("deactivate r1 must succeed, got %v", err)
	}
	if err := e.Activate("r2"); err != nil {
		t.Fatalf("activate r2 must succeed, got %v", err)
	}
	if err := e.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	r1, ok := e.Rule("r1")
	if !ok || !r1.Active {
		t.Fatalf("r1 after restore = %+v ok=%v, want an active rule", r1, ok)
	}
	r2, ok := e.Rule("r2")
	if !ok || r2.Active {
		t.Fatalf("r2 after restore = %+v ok=%v, want an inactive rule", r2, ok)
	}
	got, err := e.Render("r1", map[string]string{"path": "ok"})
	if err != nil {
		t.Fatalf("render after restore must succeed, got %v", err)
	}
	if got != "uri=\"ok\"" {
		t.Fatalf("render after restore = %q, want uri=\"ok\"", got)
	}
	if err := e.Restore([]byte("broken snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	if r1, _ := e.Rule("r1"); !r1.Active {
		t.Fatalf("failed restore must leave the registry untouched")
	}
}
