package core

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type dirListPage struct {
	dir     string
	folders []string
	page    int
	pages   int
}

func readDirListPage(dir string, page int) (dirListPage, error) {
	view := dirListPage{page: 1, pages: 1}
	if dir == "" {
		dir = "."
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return view, fmt.Errorf("dir ls: resolve directory: %w", err)
	}
	view.dir = filepath.Clean(absDir)
	entries, err := os.ReadDir(view.dir)
	if err != nil {
		return view, fmt.Errorf("dir ls: read directory: %w", err)
	}
	for _, entry := range entries {
		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			info, err := os.Stat(filepath.Join(view.dir, entry.Name()))
			if err != nil {
				slog.Debug("dir ls: skipping unreadable link", "path", filepath.Join(view.dir, entry.Name()), "error", err)
				continue
			}
			isDir = info.IsDir()
		}
		if isDir {
			view.folders = append(view.folders, entry.Name())
		}
	}
	// os.ReadDir sorts by name; filtering preserves stable pagination.
	if len(view.folders) > 0 {
		view.pages = (len(view.folders) + dirCardPageSize - 1) / dirCardPageSize
	}
	view.page = max(1, min(page, view.pages))
	return view, nil
}

func (view dirListPage) visibleFolders() []string {
	start := (view.page - 1) * dirCardPageSize
	end := min(start+dirCardPageSize, len(view.folders))
	return view.folders[start:end]
}

func dirListNavAction(dir string, page int) string {
	return fmt.Sprintf("nav:/dir ls %s %d", encodeDirListPath(dir), page)
}

func dirListSelectAction(dir, target string, page int) string {
	return fmt.Sprintf("act:/dir ls-select %s %s %d", encodeDirListPath(dir), encodeDirListPath(target), page)
}

func encodeDirListPath(path string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(path))
}

func decodeDirListPath(encoded string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 || strings.ContainsRune(string(decoded), '\x00') || !filepath.IsAbs(string(decoded)) {
		return "", fmt.Errorf("dir ls: invalid encoded absolute path")
	}
	return filepath.Clean(string(decoded)), nil
}

var dirListMarkdownEscaper = strings.NewReplacer(
	"\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]",
	"<", "&lt;", ">", "&gt;", "\r", "\\r", "\n", "\\n",
)

func (e *Engine) renderDirListCard(view dirListPage, feedback string) *Card {
	cb := NewCard().Title(e.i18n.T(MsgDirListTitle), "turquoise").
		Markdown(e.i18n.Tf(MsgDirListPath, dirListMarkdownEscaper.Replace(view.dir)))
	if feedback != "" {
		cb.Markdown(feedback)
	}
	if len(view.folders) == 0 {
		cb.Note(e.i18n.T(MsgDirListEmpty))
	} else {
		cb.Divider()
		start := (view.page - 1) * dirCardPageSize
		for i, name := range view.visibleFolders() {
			cb.ListItem(
				fmt.Sprintf("%d. %s", start+i+1, dirListMarkdownEscaper.Replace(name)),
				e.i18n.T(MsgDirListSwitch),
				dirListSelectAction(view.dir, filepath.Join(view.dir, name), view.page),
			)
		}
	}
	return cb.ButtonsEqual(
		DefaultBtn(e.i18n.T(MsgDirListHome), dirListNavAction(view.dir, 1)),
		DefaultBtn(e.i18n.T(MsgDirListPrev), dirListNavAction(view.dir, max(1, view.page-1))),
		DefaultBtn(e.i18n.T(MsgDirListNext), dirListNavAction(view.dir, min(view.pages, view.page+1))),
	).Note(e.i18n.Tf(MsgDirListPageHint, view.page, view.pages, len(view.folders))).Build()
}

func (e *Engine) dirListErrorCard(message string) *Card {
	return NewCard().Title(e.i18n.T(MsgDirListTitle), "red").Markdown(message).Build()
}

func (e *Engine) dirListPermissionError(userID string) string {
	e.userRolesMu.RLock()
	disabled := e.disabledCmds["dir"]
	roles := e.userRoles
	e.userRolesMu.RUnlock()
	if roles != nil {
		if role := roles.ResolveRole(userID); role != nil {
			disabled = role.DisabledCmds["dir"]
		}
	}
	if disabled {
		return e.i18n.Tf(MsgCommandDisabled, "/dir")
	}
	if userID == "" || !e.isAdmin(userID) {
		return e.i18n.Tf(MsgAdminRequired, "/dir")
	}
	return ""
}

func (e *Engine) cmdDirList(p Platform, msg *Message, switcher WorkDirSwitcher, args []string) {
	page := 1
	if len(args) > 1 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDirListUsage))
		return
	}
	if len(args) == 1 {
		var err error
		page, err = strconv.Atoi(args[0])
		if err != nil || page < 1 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDirListUsage))
			return
		}
	}
	view, err := readDirListPage(switcher.GetWorkDir(), page)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgDirListReadFailed, err))
		return
	}
	// In-place filesystem actions require the verified callback operator.
	if _, ok := p.(AuthenticatedCardNavigable); ok && supportsCards(p) {
		e.replyWithCard(p, msg.ReplyCtx, e.renderDirListCard(view, ""))
		return
	}
	var text strings.Builder
	fmt.Fprintln(&text, e.i18n.Tf(MsgDirListPath, view.dir))
	if len(view.folders) == 0 {
		fmt.Fprintln(&text, e.i18n.T(MsgDirListEmpty))
	}
	for i, name := range view.visibleFolders() {
		fmt.Fprintf(&text, "%d. %s\n", (view.page-1)*dirCardPageSize+i+1, filepath.Join(view.dir, name))
	}
	fmt.Fprintln(&text, e.i18n.Tf(MsgDirListPageHint, view.page, view.pages, len(view.folders)))
	text.WriteString(e.i18n.T(MsgDirListTextHint))
	e.reply(p, msg.ReplyCtx, text.String())
}

func isDirListCardArgs(args []string) bool {
	return len(args) > 0 && (args[0] == "ls" || args[0] == "ls-select")
}

func (e *Engine) handleDirListCardAction(prefix string, args []string, sessionKey, userID string) *Card {
	if message := e.dirListPermissionError(userID); message != "" {
		return e.dirListErrorCard(message)
	}
	if sessionKey == "" {
		return e.dirListErrorCard(e.i18n.T(MsgDirListUsage))
	}
	selecting := len(args) == 4 && args[0] == "ls-select" && prefix == "act"
	if !selecting && !(len(args) == 3 && args[0] == "ls" && prefix == "nav") {
		return e.dirListErrorCard(e.i18n.T(MsgDirListUsage))
	}
	dir, err := decodeDirListPath(args[1])
	if err != nil {
		return e.dirListErrorCard(e.i18n.T(MsgDirListUsage))
	}
	page, err := strconv.Atoi(args[len(args)-1])
	if err != nil {
		return e.dirListErrorCard(e.i18n.T(MsgDirListUsage))
	}
	view, err := readDirListPage(dir, page)
	if err != nil {
		return e.dirListErrorCard(e.i18n.Tf(MsgDirListReadFailed, err))
	}
	if !selecting {
		return e.renderDirListCard(view, "")
	}
	target, err := decodeDirListPath(args[2])
	if err != nil || filepath.Dir(target) != view.dir {
		return e.renderDirListCard(view, e.i18n.T(MsgDirListUsage))
	}
	nextView, err := readDirListPage(target, 1)
	if err != nil {
		return e.renderDirListCard(view, e.i18n.Tf(MsgDirListReadFailed, err))
	}
	agent, sessions := e.sessionContextForKey(sessionKey)
	errMsg, successMsg := e.dirApply(agent, sessions, e.interactiveKeyForSessionKey(sessionKey), sessionKey, []string{target})
	if errMsg != "" {
		return e.renderDirListCard(view, errMsg)
	}
	return e.renderDirListCard(nextView, successMsg)
}
