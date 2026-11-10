package core

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

type dirListTestPlatform struct {
	stubCardPlatform
	cardNav AuthenticatedCardNavigationHandler
}

func (p *dirListTestPlatform) SetAuthenticatedCardNavigationHandler(handler AuthenticatedCardNavigationHandler) {
	p.cardNav = handler
}

func (p *dirListTestPlatform) lastReply(t *testing.T) *Card {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.repliedCards) == 0 {
		t.Fatal("no card reply")
	}
	return p.repliedCards[len(p.repliedCards)-1]
}

func dirListTestEngine(t *testing.T, dir string, lang Language) (*Engine, *stubWorkDirAgent, *dirListTestPlatform) {
	t.Helper()
	p := &dirListTestPlatform{stubCardPlatform: stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}
	agent := &stubWorkDirAgent{workDir: dir}
	dataDir := t.TempDir()
	e := NewEngine("test", agent, []Platform{p}, filepath.Join(dataDir, "sessions.json"), lang)
	e.SetAdminFrom("owner")
	e.SetDirHistory(NewDirHistory(dataDir))
	e.initPlatformCapabilities(p)
	t.Cleanup(func() { _ = e.Stop() })
	return e, agent, p
}

func dirListFixture(t *testing.T, count int) string {
	t.Helper()
	dir := t.TempDir()
	for i := 0; i < count; i++ {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("folder-%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func dirListItems(card *Card) []CardListItem {
	var rows []CardListItem
	for _, element := range card.Elements {
		if row, ok := element.(CardListItem); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

func dirListNavButtons(t *testing.T, card *Card) []CardButton {
	t.Helper()
	var rows []CardActions
	for _, element := range card.Elements {
		if row, ok := element.(CardActions); ok {
			rows = append(rows, row)
		}
	}
	if len(rows) != 1 || rows[0].Layout != CardActionLayoutEqualColumns || len(rows[0].Buttons) != 3 {
		t.Fatalf("navigation = %#v, want exactly one three-column row", rows)
	}
	return rows[0].Buttons
}

func TestReadDirListPage_ListsOnlyDirectFolders(t *testing.T) {
	dir := t.TempDir()
	want := []string{".hidden", "2", "named space", "ls", "name_[test]", "\u4e2d\u6587"}
	for _, name := range want {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "named space", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ordinary.txt"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	view, err := readDirListPage(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(view.folders, want) {
		t.Fatalf("folders = %q, want %q", view.folders, want)
	}
}

func TestReadDirListPage_PaginationAndClamping(t *testing.T) {
	for _, tc := range []struct {
		count, requested, page, pages, visible int
	}{
		{0, 1, 1, 1, 0}, {1, 1, 1, 1, 1}, {20, 1, 1, 1, 20},
		{21, 1, 1, 2, 20}, {21, 2, 2, 2, 1}, {41, 2, 2, 3, 20},
		{41, 99999, 3, 3, 1}, {41, -1, 1, 3, 20},
	} {
		t.Run(fmt.Sprintf("%d-folders-page-%d", tc.count, tc.requested), func(t *testing.T) {
			view, err := readDirListPage(dirListFixture(t, tc.count), tc.requested)
			if err != nil {
				t.Fatal(err)
			}
			if view.page != tc.page || view.pages != tc.pages || len(view.visibleFolders()) != tc.visible {
				t.Fatalf("page=%d/%d rows=%d, want %d/%d rows=%d", view.page, view.pages, len(view.visibleFolders()), tc.page, tc.pages, tc.visible)
			}
		})
	}
}

func TestReadDirListPage_DirectoryLinksAreIncluded(t *testing.T) {
	dir := dirListFixture(t, 1)
	if err := os.Symlink(filepath.Join(dir, "folder-00"), filepath.Join(dir, "dir-link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"file-link": "file.txt", "broken-link": "missing"} {
		if err := os.Symlink(filepath.Join(dir, target), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	view, err := readDirListPage(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(view.folders, []string{"dir-link", "folder-00"}) {
		t.Fatalf("folders = %q", view.folders)
	}
}

func TestDirListPaths_RoundTripAndRejectMalformedValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "named space_[x]_\u4e2d\u6587")
	decoded, err := decodeDirListPath(encodeDirListPath(path))
	if err != nil || decoded != path {
		t.Fatalf("decoded=%q err=%v, want %q", decoded, err, path)
	}
	for _, value := range []string{"", "%%%", encodeDirListPath("relative"), encodeDirListPath(path + "\x00")} {
		if _, err := decodeDirListPath(value); err == nil {
			t.Fatalf("accepted invalid path %q", value)
		}
	}
}

func TestCmdDirList_UsesLiveFoldersAndFixedChineseNavigation(t *testing.T) {
	dir := dirListFixture(t, 21)
	e, agent, p := dirListTestEngine(t, dir, LangChinese)
	e.dirHistory.Add("test", t.TempDir())
	e.cmdDir(p, &Message{SessionKey: "test:owner", ReplyCtx: "ctx"}, []string{"ls"})
	card := p.lastReply(t)
	if rows := dirListItems(card); len(rows) != 20 || !strings.Contains(rows[0].Text, "folder-00") {
		t.Fatalf("folder rows = %#v", rows)
	}
	buttons := dirListNavButtons(t, card)
	for i, want := range []string{"\u9996\u9875", "\u4e0a\u4e00\u9875", "\u4e0b\u4e00\u9875"} {
		if buttons[i].Text != want {
			t.Fatalf("button %d = %q, want %q", i, buttons[i].Text, want)
		}
	}
	if buttons[0].Value != buttons[1].Value || buttons[2].Value != dirListNavAction(dir, 2) {
		t.Fatalf("first-page actions = %#v", buttons)
	}
	if agent.workDir != dir || countCardActionValues(card, "act:/dir reset") != 0 || countCardActionValues(card, "nav:/help") != 0 {
		t.Fatal("listing changed directory or included history/menu controls")
	}
}

func TestCmdDirList_InvalidPageDoesNotSwitchToLsFolder(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "ls"), 0o755); err != nil {
		t.Fatal(err)
	}
	e, agent, p := dirListTestEngine(t, dir, LangEnglish)
	for _, args := range [][]string{{"ls", "0"}, {"ls", "-1"}, {"ls", "bad"}, {"ls", "2", "extra"}} {
		e.cmdDir(p, &Message{SessionKey: "test:owner", ReplyCtx: "ctx"}, args)
		if agent.workDir != dir {
			t.Fatal("invalid list command switched the directory")
		}
	}
	if len(p.sent) != 4 || !strings.Contains(p.sent[0], "/dir ls [page]") {
		t.Fatalf("usage replies = %q", p.sent)
	}
	e.cmdDir(p, &Message{SessionKey: "test:owner", ReplyCtx: "ctx"}, []string{"./ls"})
	if agent.workDir != filepath.Join(dir, "ls") {
		t.Fatal("explicit ./ls path no longer works")
	}
}

func TestDirListCardAction_PagingPreservesSessionHistoryAndRetry(t *testing.T) {
	dir := dirListFixture(t, 41)
	e, agent, p := dirListTestEngine(t, dir, LangEnglish)
	sk := "test:owner"
	session := e.sessions.GetOrCreateActive(sk)
	session.SetAgentSessionID("existing", "test")
	session.AddHistory("user", "keep this context")
	e.sessions.Save()
	e.dirHistory.Add("test", dir)
	before, err := os.ReadFile(e.dirHistory.storePath)
	if err != nil {
		t.Fatal(err)
	}
	retry := e.beginAutoRetry("retry", session, e.sessions)
	e.ReceiveMessage(p, &Message{SessionKey: sk, UserID: "owner", Platform: "test", ReplyCtx: "ctx", Content: "/dir ls 2"})
	card := p.lastReply(t)
	for _, action := range []string{dirListNavAction(dir, 3), dirListNavAction(dir, 1), dirListNavAction(dir, 99999)} {
		card = p.cardNav(action, sk, "owner")
		if card == nil {
			t.Fatal("navigation returned no card")
		}
	}
	buttons := dirListNavButtons(t, card)
	if buttons[2].Value != dirListNavAction(dir, 3) || !strings.Contains(card.RenderText(), "Page 3/3 (41 folders)") {
		t.Fatal("last-page next action did not stay on the last page")
	}
	after, err := os.ReadFile(e.dirHistory.storePath)
	if err != nil {
		t.Fatal(err)
	}
	if agent.workDir != dir || session.GetAgentSessionID() != "existing" || len(session.GetHistory(0)) != 1 || !bytes.Equal(before, after) || retry.ctx.Err() != nil {
		t.Fatal("pure navigation mutated the work directory, session, history or pending retry")
	}
}

func TestDirListCardAction_StaleNavigationUsesCardDirectory(t *testing.T) {
	dir := dirListFixture(t, 21)
	e, agent, p := dirListTestEngine(t, dir, LangEnglish)
	view, err := readDirListPage(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	card := e.renderDirListCard(view, "")
	other := dirListFixture(t, 1)
	agent.SetWorkDir(other)
	card = p.cardNav(dirListNavButtons(t, card)[0].Value, "test:owner", "owner")
	if agent.workDir != other || !strings.Contains(card.RenderText(), "Page 1/2 (21 folders)") {
		t.Fatal("stale Home action used or changed the current directory")
	}
	for _, button := range dirListNavButtons(t, card) {
		if !strings.Contains(button.Value, encodeDirListPath(dir)) {
			t.Fatalf("navigation no longer targets the card's directory: %q", button.Value)
		}
	}
}

func TestDirListCardAction_SelectionUsesBoundPathNotHistoryOrCurrentDirectory(t *testing.T) {
	for _, name := range []string{"2", "named space", "\u4e2d\u6587", "ls", "name_[x]"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, name)
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
			e, agent, p := dirListTestEngine(t, dir, LangEnglish)
			view, err := readDirListPage(dir, 1)
			if err != nil {
				t.Fatal(err)
			}
			action := dirListItems(e.renderDirListCard(view, ""))[0].BtnValue
			other := t.TempDir()
			agent.SetWorkDir(other)
			e.dirHistory.Add("test", other)
			sk := "test:owner"
			session := e.sessions.GetOrCreateActive(sk)
			session.SetAgentSessionID("existing", "test")
			session.AddHistory("user", "old directory context")
			retry := e.beginAutoRetry("pending", session, e.sessions)
			card := p.cardNav(action, sk, "owner")
			if agent.workDir != target || session.GetAgentSessionID() != "" || len(session.GetHistory(0)) != 0 {
				t.Fatal("selection did not use the absolute target and existing switch semantics")
			}
			if retry.ctx.Err() == nil {
				t.Fatal("successful selection did not cancel the old directory's pending retry")
			}
			if e.dirHistory.Get("test", 1) != target || !strings.Contains(card.RenderText(), "Page 1/1 (0 folders)") {
				t.Fatal("selection did not record history and display the new directory's first page")
			}
		})
	}
}

func TestDirListCardAction_RejectsMissingFilesAndOutsideTargets(t *testing.T) {
	dir := dirListFixture(t, 1)
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e, agent, p := dirListTestEngine(t, dir, LangEnglish)
	session := e.sessions.GetOrCreateActive("test:owner")
	session.SetAgentSessionID("existing", "test")
	session.AddHistory("user", "keep this context")
	retry := e.beginAutoRetry("pending", session, e.sessions)
	for _, target := range []string{filepath.Join(dir, "missing"), file, t.TempDir()} {
		card := p.cardNav(dirListSelectAction(dir, target, 1), "test:owner", "owner")
		if card == nil || agent.workDir != dir || !strings.Contains(card.RenderText(), "folder-00") {
			t.Fatal("failed selection changed directory or discarded the original list")
		}
		if session.GetAgentSessionID() != "existing" || len(session.GetHistory(0)) != 1 || retry.ctx.Err() != nil {
			t.Fatal("failed selection changed the session or pending retry")
		}
	}
}

func TestDirListCardAction_VerifiesActorAndCommandPolicy(t *testing.T) {
	dir := dirListFixture(t, 1)
	e, agent, p := dirListTestEngine(t, dir, LangEnglish)
	action := dirListSelectAction(dir, filepath.Join(dir, "folder-00"), 1)
	for _, actor := range []string{"guest", ""} {
		card := p.cardNav(action, "test:owner", actor)
		if !strings.Contains(strings.ToLower(card.RenderText()), "admin") || len(dirListItems(card)) != 0 || agent.workDir != dir {
			t.Fatal("unverified operator inherited the card owner's permissions")
		}
	}
	e.SetDisabledCommands([]string{"dir"})
	if card := p.cardNav(action, "test:owner", "owner"); !strings.Contains(card.RenderText(), "disabled") || agent.workDir != dir {
		t.Fatal("disabled command was bypassed by a button")
	}
	e.SetDisabledCommands(nil)
	roles := NewUserRoleManager()
	roles.Configure("blocked", []RoleInput{{Name: "blocked", UserIDs: []string{"owner"}, DisabledCommands: []string{"dir"}}})
	e.SetUserRoles(roles)
	if card := p.cardNav(dirListNavAction(dir, 1), "test:owner", "owner"); !strings.Contains(card.RenderText(), "disabled") {
		t.Fatal("navigation bypassed the operator's role policy")
	}
	if card := e.handleCardNav(action, "test:owner"); card != nil || agent.workDir != dir {
		t.Fatal("unauthenticated callback fallback was allowed")
	}
}

func TestDirListCardAction_RejectsMalformedCallbacks(t *testing.T) {
	dir := dirListFixture(t, 1)
	_, agent, p := dirListTestEngine(t, dir, LangEnglish)
	for _, action := range []string{
		"nav:/dir ls", "nav:/dir ls %%% 1", "nav:/dir ls " + encodeDirListPath("relative") + " 1",
		"nav:/dir ls " + encodeDirListPath(dir) + " not-a-page",
		"act:/dir ls " + encodeDirListPath(dir) + " 1", "nav:/dir ls-select " + encodeDirListPath(dir) + " 1",
	} {
		card := p.cardNav(action, "test:owner", "owner")
		if card == nil || !strings.Contains(card.RenderText(), "/dir ls [page]") || agent.workDir != dir {
			t.Fatalf("malformed action %q did not fail safely", action)
		}
	}
}

func TestCmdDirList_UnverifiedPlatformsHaveReadableTextFallback(t *testing.T) {
	dir := dirListFixture(t, 1)
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "plain"}}
	e := NewEngine("test", &stubWorkDirAgent{workDir: dir}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.cmdDir(p, &Message{SessionKey: "test:owner", ReplyCtx: "ctx"}, []string{"ls"})
	if len(p.repliedCards) != 0 || len(p.sent) != 1 || !strings.Contains(p.sent[0], "folder-00") || !strings.Contains(p.sent[0], "/dir ls <page>") {
		t.Fatal("platform without verified card callbacks received unusable buttons")
	}
}

func TestDirListMessages_AllLanguages(t *testing.T) {
	for _, key := range []MsgKey{
		MsgDirListTitle, MsgDirListPath, MsgDirListHome, MsgDirListPrev, MsgDirListNext,
		MsgDirListSwitch, MsgDirListPageHint, MsgDirListEmpty, MsgDirListReadFailed,
		MsgDirListUsage, MsgDirListTextHint,
	} {
		for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
			if messages[key][lang] == "" {
				t.Errorf("missing %s translation for %s", lang, key)
			}
		}
	}
}

func TestDirListCardAction_MissingListedDirectoryPreservesSessionAndRetry(t *testing.T) {
	dir := dirListFixture(t, 1)
	e, agent, p := dirListTestEngine(t, dir, LangEnglish)
	sk := "test:owner"
	session := e.sessions.GetOrCreateActive(sk)
	session.SetAgentSessionID("existing", "test")
	session.AddHistory("user", "keep this context")
	retry := e.beginAutoRetry("pending", session, e.sessions)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	card := p.cardNav(dirListNavAction(dir, 1), sk, "owner")
	if card == nil || len(dirListItems(card)) != 0 || agent.workDir != dir {
		t.Fatal("missing listed directory did not fail without switching")
	}
	if session.GetAgentSessionID() != "existing" || len(session.GetHistory(0)) != 1 || retry.ctx.Err() != nil {
		t.Fatal("missing listed directory changed the session or pending retry")
	}
}

func TestDirListCardAction_UnreadableChildPreservesSessionAndRetry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions are required")
	}
	dir := dirListFixture(t, 1)
	target := filepath.Join(dir, "folder-00")
	if err := os.Chmod(target, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0o755) })
	if _, err := os.ReadDir(target); err == nil {
		t.Skip("run without DAC override capabilities to exercise unreadable directories")
	}
	e, agent, p := dirListTestEngine(t, dir, LangEnglish)
	sk := "test:owner"
	session := e.sessions.GetOrCreateActive(sk)
	session.SetAgentSessionID("existing", "test")
	session.AddHistory("user", "keep this context")
	retry := e.beginAutoRetry("pending", session, e.sessions)
	card := p.cardNav(dirListSelectAction(dir, target, 1), sk, "owner")
	if card == nil || agent.workDir != dir || !strings.Contains(card.RenderText(), "folder-00") {
		t.Fatal("unreadable child discarded the original list or switched directories")
	}
	if session.GetAgentSessionID() != "existing" || len(session.GetHistory(0)) != 1 || retry.ctx.Err() != nil {
		t.Fatal("unreadable child changed the session or pending retry")
	}
}

func TestDirListCardAction_UnsupportedAgentPreservesPendingRetry(t *testing.T) {
	dir := dirListFixture(t, 1)
	e, agent, p := dirListTestEngine(t, dir, LangEnglish)
	sk := "test:owner"
	session := e.sessions.GetOrCreateActive(sk)
	session.SetAgentSessionID("existing", "test")
	session.AddHistory("user", "keep this context")
	retry := e.beginAutoRetry("pending", session, e.sessions)
	// A saved card can outlive the work-directory capability of its agent.
	e.agent = &stubAgent{}
	card := p.cardNav(dirListSelectAction(dir, filepath.Join(dir, "folder-00"), 1), sk, "owner")
	if card == nil || !strings.Contains(card.RenderText(), e.i18n.T(MsgDirNotSupported)) || agent.workDir != dir {
		t.Fatal("unsupported agent did not retain the original list with an error")
	}
	if session.GetAgentSessionID() != "existing" || len(session.GetHistory(0)) != 1 || retry.ctx.Err() != nil {
		t.Fatal("failed directory application changed the session or pending retry")
	}
}

func TestDirListCardAction_RealFilesystemSmoke(t *testing.T) {
	dir := os.Getenv("CC_DIR_LIST_SMOKE_DIR")
	if dir == "" {
		t.Skip("an explicit read-only smoke directory is required")
	}
	if !filepath.IsAbs(dir) {
		t.Fatal("smoke directory must be absolute")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var folders []string
	for _, entry := range entries {
		info, err := os.Stat(filepath.Join(dir, entry.Name()))
		if err == nil && info.IsDir() {
			folders = append(folders, entry.Name())
		}
	}
	e, agent, p := dirListTestEngine(t, dir, LangEnglish)
	sk := "test:owner"
	e.ReceiveMessage(p, &Message{SessionKey: sk, UserID: "owner", Platform: "test", ReplyCtx: "ctx", Content: "/dir ls"})
	card := p.lastReply(t)
	pages := max(1, (len(folders)+dirCardPageSize-1)/dirCardPageSize)
	var selectAction string
	for page := 1; page <= pages; page++ {
		rows := dirListItems(card)
		start := (page - 1) * dirCardPageSize
		end := min(start+dirCardPageSize, len(folders))
		if len(rows) != end-start {
			t.Fatalf("page %d has %d rows, want %d", page, len(rows), end-start)
		}
		for i, name := range folders[start:end] {
			if rows[i].BtnValue != dirListSelectAction(dir, filepath.Join(dir, name), page) {
				t.Fatal("live folder did not receive a card-bound absolute selection")
			}
			if name == "cc-connect" {
				selectAction = rows[i].BtnValue
			}
		}
		if agent.workDir != dir {
			t.Fatal("live-directory paging changed the workdir")
		}
		if page < pages {
			card = p.cardNav(dirListNavButtons(t, card)[2].Value, sk, "owner")
		}
	}
	if selectAction == "" {
		t.Fatal("the explicitly selected smoke directory lacks cc-connect")
	}
	card = p.cardNav(selectAction, sk, "owner")
	if card == nil || agent.workDir != filepath.Join(dir, "cc-connect") {
		t.Fatal("live-folder selection did not change the isolated mock agent's workdir")
	}
	t.Logf("read-only smoke: %d real folders across %d pages; isolated mock selection passed", len(folders), pages)
}
