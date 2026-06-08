// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — mail_db.go
// Read-only access to the macOS Mail "Envelope Index" SQLite database. It indexes
// every message across accounts (subjects, senders, mailboxes, read/flag state).
// FDA-gated. The schema drifts across Mail versions, so the message query is
// assembled from columns detected at runtime rather than hard-coded.
package cli

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// defaultMailIndexPath finds the newest Mail "V*" data directory and returns its
// Envelope Index path. Apple bumps the V-number across major macOS releases
// (…V9, V10), so we pick the highest rather than hard-coding one.
func defaultMailIndexPath() string {
	home, _ := os.UserHomeDir()
	mailDir := filepath.Join(home, "Library", "Mail")
	entries, err := os.ReadDir(mailDir)
	if err != nil {
		return filepath.Join(mailDir, "V10", "MailData", "Envelope Index")
	}
	best := ""
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "V") {
			if e.Name() > best {
				best = e.Name()
			}
		}
	}
	if best == "" {
		best = "V10"
	}
	return filepath.Join(mailDir, best, "MailData", "Envelope Index")
}

func openMailIndex(path string) (*sql.DB, error) {
	if runtime.GOOS != "darwin" {
		return nil, configErr(fmt.Errorf("icloud-pp-cli mail requires macOS — this is %s", runtime.GOOS))
	}
	if path == "" {
		path = defaultMailIndexPath()
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("Mail Envelope Index not found at %s (is Mail.app set up?)", path)
		}
		if isPermissionError(err) {
			return nil, configErr(fmt.Errorf("%w: %s", errFDADenied, path))
		}
		return nil, err
	}
	u := &url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_busy_timeout=5000&_query_only=1"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("cannot open Mail index: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		if isPermissionError(err) {
			return nil, configErr(fmt.Errorf("%w: %s", errFDADenied, path))
		}
		return nil, fmt.Errorf("cannot read Mail index: %w", err)
	}
	return db, nil
}

// ── types ─────────────────────────────────────────────────────────────────────

// MailMessage is one indexed message.
type MailMessage struct {
	Subject     string `json:"subject"`
	FromAddress string `json:"from_address,omitempty"`
	FromName    string `json:"from_name,omitempty"`
	Date        string `json:"date,omitempty"`
	Read        bool   `json:"read"`
	Flagged     bool   `json:"flagged"`
	Mailbox     string `json:"mailbox,omitempty"`
}

// MailFilter narrows a message query.
type MailFilter struct {
	UnreadOnly  bool
	FlaggedOnly bool
	From        string
	Since       time.Time
	Limit       int
}

// mailCols records which optional columns the messages table exposes in this
// Mail version, so queries only reference columns that exist.
type mailCols struct {
	hasRead    bool
	hasFlagged bool
	hasDeleted bool
	hasFlags   bool
}

func detectMailColumns(db *sql.DB) (mailCols, error) {
	cols, err := tableColumns(db, "messages")
	if err != nil {
		return mailCols{}, err
	}
	if len(cols) == 0 {
		return mailCols{}, fmt.Errorf("Mail index has no 'messages' table — unsupported schema")
	}
	has := func(c string) bool { _, ok := cols[c]; return ok }
	return mailCols{
		hasRead:    has("read"),
		hasFlagged: has("flagged"),
		hasDeleted: has("deleted"),
		hasFlags:   has("flags"),
	}, nil
}

// readExpr / flaggedExpr return SQL that yields a 0/1 read/flagged value across
// schema variants: a dedicated column when present, otherwise a bit of `flags`
// (bit 0 = read, bit 4 = flagged in Mail's flag layout), otherwise constant 0.
func (mc mailCols) readExpr() string {
	switch {
	case mc.hasRead:
		return "COALESCE(m.read, 0)"
	case mc.hasFlags:
		return "(m.flags & 1)"
	default:
		return "0"
	}
}

func (mc mailCols) flaggedExpr() string {
	switch {
	case mc.hasFlagged:
		return "COALESCE(m.flagged, 0)"
	case mc.hasFlags:
		return "((m.flags & 16) <> 0)"
	default:
		return "0"
	}
}

// ── queries ───────────────────────────────────────────────────────────────────

func queryMail(db *sql.DB, mc mailCols, filter MailFilter) ([]MailMessage, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	conds := []string{"1 = 1"}
	var args []any
	if mc.hasDeleted {
		conds = append(conds, "COALESCE(m.deleted, 0) = 0")
	}
	if filter.UnreadOnly {
		conds = append(conds, mc.readExpr()+" = 0")
	}
	if filter.FlaggedOnly {
		conds = append(conds, mc.flaggedExpr()+" <> 0")
	}
	if filter.From != "" {
		conds = append(conds, "(LOWER(a.address) LIKE ? OR LOWER(a.comment) LIKE ?)")
		like := "%" + strings.ToLower(filter.From) + "%"
		args = append(args, like, like)
	}
	if !filter.Since.IsZero() {
		conds = append(conds, "m.date_received >= ?")
		args = append(args, filter.Since.Unix())
	}
	q := fmt.Sprintf(`
		SELECT COALESCE(s.subject, '(no subject)'),
		       COALESCE(a.address, ''),
		       COALESCE(a.comment, ''),
		       COALESCE(m.date_received, 0),
		       %s, %s,
		       COALESCE(mb.url, '')
		FROM messages m
		LEFT JOIN subjects s  ON s.ROWID = m.subject
		LEFT JOIN addresses a ON a.ROWID = m.sender
		LEFT JOIN mailboxes mb ON mb.ROWID = m.mailbox
		WHERE %s
		ORDER BY m.date_received DESC
		LIMIT %d
	`, mc.readExpr(), mc.flaggedExpr(), joinConds(conds), limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query mail: %w", err)
	}
	defer rows.Close()
	return scanMail(rows)
}

func searchMail(db *sql.DB, mc mailCols, query string, limit int) ([]MailMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	like := "%" + strings.ToLower(query) + "%"
	q := fmt.Sprintf(`
		SELECT COALESCE(s.subject, '(no subject)'),
		       COALESCE(a.address, ''),
		       COALESCE(a.comment, ''),
		       COALESCE(m.date_received, 0),
		       %s, %s,
		       COALESCE(mb.url, '')
		FROM messages m
		LEFT JOIN subjects s  ON s.ROWID = m.subject
		LEFT JOIN addresses a ON a.ROWID = m.sender
		LEFT JOIN mailboxes mb ON mb.ROWID = m.mailbox
		WHERE LOWER(s.subject) LIKE ? OR LOWER(a.address) LIKE ? OR LOWER(a.comment) LIKE ?
		ORDER BY m.date_received DESC
		LIMIT %d
	`, mc.readExpr(), mc.flaggedExpr(), limit)
	rows, err := db.Query(q, like, like, like)
	if err != nil {
		return nil, fmt.Errorf("search mail: %w", err)
	}
	defer rows.Close()
	return scanMail(rows)
}

func scanMail(rows *sql.Rows) ([]MailMessage, error) {
	var out []MailMessage
	for rows.Next() {
		var m MailMessage
		var subject, addr, name, mailbox sql.NullString
		var date float64
		var read, flagged sql.NullInt64
		if err := rows.Scan(&subject, &addr, &name, &date, &read, &flagged, &mailbox); err != nil {
			return nil, err
		}
		m.Subject, m.FromAddress, m.FromName = subject.String, addr.String, name.String
		m.Read, m.Flagged = read.Int64 == 1, flagged.Int64 == 1
		m.Mailbox = mailboxLabel(mailbox.String)
		if date > 0 {
			m.Date = time.Unix(int64(date), 0).UTC().Format(time.RFC3339)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ── mailboxes + analytics ─────────────────────────────────────────────────────

// Mailbox is one account/folder with its message counts.
type Mailbox struct {
	Name   string `json:"name"`
	URL    string `json:"url,omitempty"`
	Total  int64  `json:"total"`
	Unread int64  `json:"unread"`
}

func queryMailboxes(db *sql.DB) ([]Mailbox, error) {
	cols, _ := tableColumns(db, "mailboxes")
	totalCol := "total_count"
	unreadCol := "unread_count"
	if _, ok := cols["unread_count"]; !ok {
		if _, ok2 := cols["unseen_count"]; ok2 {
			unreadCol = "unseen_count"
		}
	}
	if _, ok := cols[totalCol]; !ok {
		totalCol = "0"
	}
	if _, ok := cols[unreadCol]; !ok && unreadCol != "0" {
		unreadCol = "0"
	}
	q := fmt.Sprintf(`SELECT COALESCE(url, ''), COALESCE(%s, 0), COALESCE(%s, 0) FROM mailboxes ORDER BY %s DESC`,
		totalCol, unreadCol, totalCol)
	rows, err := db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("query mailboxes: %w", err)
	}
	defer rows.Close()
	var out []Mailbox
	for rows.Next() {
		var mb Mailbox
		if err := rows.Scan(&mb.URL, &mb.Total, &mb.Unread); err != nil {
			return nil, err
		}
		mb.Name = mailboxLabel(mb.URL)
		out = append(out, mb)
	}
	return out, rows.Err()
}

// MailSenderStat ranks a sender by message count.
type MailSenderStat struct {
	Address string `json:"address"`
	Name    string `json:"name,omitempty"`
	Count   int64  `json:"count"`
}

func topMailSenders(db *sql.DB, limit int) ([]MailSenderStat, error) {
	if limit <= 0 {
		limit = 15
	}
	q := fmt.Sprintf(`
		SELECT COALESCE(a.address, ''), COALESCE(a.comment, ''), COUNT(*) AS cnt
		FROM messages m JOIN addresses a ON a.ROWID = m.sender
		WHERE a.address IS NOT NULL AND a.address != ''
		GROUP BY m.sender ORDER BY cnt DESC LIMIT %d
	`, limit)
	rows, err := db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("top senders: %w", err)
	}
	defer rows.Close()
	var out []MailSenderStat
	for rows.Next() {
		var s MailSenderStat
		if err := rows.Scan(&s.Address, &s.Name, &s.Count); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MailOverview is the headline summary.
type MailOverview struct {
	Total   int64 `json:"total"`
	Unread  int64 `json:"unread"`
	Flagged int64 `json:"flagged"`
	Senders int64 `json:"distinct_senders"`
}

func mailOverview(db *sql.DB, mc mailCols) (*MailOverview, error) {
	var o MailOverview
	deletedCond := ""
	if mc.hasDeleted {
		deletedCond = "WHERE COALESCE(m.deleted, 0) = 0"
	}
	q := fmt.Sprintf(`
		SELECT COUNT(*),
		       SUM(CASE WHEN %s = 0 THEN 1 ELSE 0 END),
		       SUM(CASE WHEN %s <> 0 THEN 1 ELSE 0 END),
		       COUNT(DISTINCT m.sender)
		FROM messages m %s
	`, mc.readExpr(), mc.flaggedExpr(), deletedCond)
	if err := db.QueryRow(q).Scan(&o.Total, &o.Unread, &o.Flagged, &o.Senders); err != nil {
		return nil, err
	}
	return &o, nil
}

// mailboxLabel turns a mailbox URL into a readable name. Mail stores URLs like
// "imap://user@host/INBOX" or "ews://…/Sent"; we surface the trailing folder and
// the account host where possible.
func mailboxLabel(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	// Folder = last path segment.
	folder := rawURL
	if i := strings.LastIndex(rawURL, "/"); i >= 0 && i < len(rawURL)-1 {
		folder = rawURL[i+1:]
	}
	folder = strings.ReplaceAll(folder, "%20", " ")
	return folder
}
