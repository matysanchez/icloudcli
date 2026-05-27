// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — messages_audit.go
// Deeper aggregate analysis of the iMessage corpus. Complements `messages stats`
// (which surfaces top-level totals) with conversation-level insight: unique
// conversations, longest threads by message count and by date span, activity
// recency, message-distribution percentiles, and from-me vs from-others split.
package cli

import (
	"database/sql"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spf13/cobra"
)

func newMessagesAuditCmd(f *rootFlags) *cobra.Command {
	var topN int
	var includeTapbacks bool

	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Audit your iMessage corpus — unique conversations, longest threads, activity",
		Long: `Audit produces a deeper analysis of your iMessage history than 'messages stats'.

It surfaces:
  - Unique conversations (DMs vs Groups, only counting chats with ≥1 real message)
  - Longest threads by message count (top N)
  - Longest threads by date span — first message to last (top N)
  - Activity recency (chats active in last 30 / 90 / 365 days; dormant chats)
  - Per-chat message distribution (avg, median, p90, max)
  - From me vs from others (total and per-chat ratio)

All counts exclude tapbacks (reaction rows) by default — use --include-tapbacks
to include them. Use --top N to control how many chats are listed in each
"longest" section (default 5).`,
		Example: `  icloud-pp-cli messages audit
  icloud-pp-cli messages audit --top 10
  icloud-pp-cli messages audit --agent | jq .`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := openMessagesDB(f.messagesDBPath)
			if err != nil {
				return err
			}
			defer db.Close()

			result, err := runAudit(db, topN, includeTapbacks)
			if err != nil {
				return err
			}

			if f.asJSON || !isTerminal(cmd.OutOrStdout()) {
				return printJSON(cmd.OutOrStdout(), result)
			}
			return printAuditTable(cmd.OutOrStdout(), f, result)
		},
	}

	cmd.Flags().IntVar(&topN, "top", 5, "Number of chats to include in 'longest' breakdowns")
	cmd.Flags().BoolVar(&includeTapbacks, "include-tapbacks", false, "Count tapback rows (default: excluded)")

	return cmd
}

// ── types ─────────────────────────────────────────────────────────────────────

// AuditResult is the full audit payload. JSON-serializable for --agent output.
type AuditResult struct {
	Conversations      AuditConversations   `json:"conversations"`
	LongestByCount     []AuditChatStat      `json:"longest_by_message_count,omitempty"`
	LongestBySpan      []AuditChatStat      `json:"longest_by_date_span,omitempty"`
	Activity           AuditActivity        `json:"activity"`
	MessageDistribution AuditDistribution   `json:"message_distribution"`
	Direction          AuditDirection       `json:"direction"`
}

// AuditConversations counts chats that have at least one real message,
// classified DM (1 participant) vs Group (2+) per chat_handle_join.
type AuditConversations struct {
	Unique int64 `json:"unique"`
	DMs    int64 `json:"dms"`
	Groups int64 `json:"groups"`
}

// AuditChatStat is one row in a "longest threads" ranking.
type AuditChatStat struct {
	GUID            string  `json:"guid"`
	ChatIdentifier  string  `json:"chat_identifier"`
	DisplayName     string  `json:"display_name,omitempty"`
	Participants    int     `json:"participants"`
	Kind            string  `json:"kind"` // "dm" or "group"
	MessageCount    int64   `json:"message_count"`
	FirstMessageDate *time.Time `json:"first_message_date,omitempty"`
	LastMessageDate  *time.Time `json:"last_message_date,omitempty"`
	SpanDays        int     `json:"span_days"`
}

// AuditActivity reports how many chats had a real message within recent
// time windows, plus the dormant count (no message in the last 365 days).
type AuditActivity struct {
	Last30Days  int64 `json:"last_30_days"`
	Last90Days  int64 `json:"last_90_days"`
	Last365Days int64 `json:"last_365_days"`
	Dormant     int64 `json:"dormant"`
}

// AuditDistribution summarizes the messages-per-chat distribution.
type AuditDistribution struct {
	AvgPerChat    float64 `json:"avg_per_chat"`
	MedianPerChat int64   `json:"median_per_chat"`
	P90PerChat    int64   `json:"p90_per_chat"`
	MaxPerChat    int64   `json:"max_per_chat"`
}

// AuditDirection splits total messages by who sent them.
type AuditDirection struct {
	FromMe         int64   `json:"from_me"`
	FromOthers     int64   `json:"from_others"`
	FromMePercent  float64 `json:"from_me_percent"`
}

// ── runner ────────────────────────────────────────────────────────────────────

func runAudit(db *sql.DB, topN int, includeTapbacks bool) (AuditResult, error) {
	if topN <= 0 {
		topN = 5
	}

	var r AuditResult

	conv, err := queryAuditConversations(db, includeTapbacks)
	if err != nil {
		return r, err
	}
	r.Conversations = conv

	byCount, err := queryAuditLongestByCount(db, topN, includeTapbacks)
	if err != nil {
		return r, err
	}
	r.LongestByCount = byCount

	bySpan, err := queryAuditLongestBySpan(db, topN, includeTapbacks)
	if err != nil {
		return r, err
	}
	r.LongestBySpan = bySpan

	activity, err := queryAuditActivity(db, includeTapbacks)
	if err != nil {
		return r, err
	}
	r.Activity = activity

	dist, err := queryAuditDistribution(db, includeTapbacks)
	if err != nil {
		return r, err
	}
	r.MessageDistribution = dist

	dir, err := queryAuditDirection(db, includeTapbacks)
	if err != nil {
		return r, err
	}
	r.Direction = dir

	return r, nil
}

// ── queries ───────────────────────────────────────────────────────────────────

// tapbackCondition returns the SQL fragment to filter out tapback rows when
// the caller has not opted to include them. The leading "AND" or "WHERE" is
// chosen by the caller via the prefix argument.
func tapbackCondition(includeTapbacks bool, prefix string) string {
	if includeTapbacks {
		return ""
	}
	return prefix + " m.associated_message_guid IS NULL"
}

func queryAuditConversations(db *sql.DB, includeTapbacks bool) (AuditConversations, error) {
	var c AuditConversations
	q := fmt.Sprintf(`
		SELECT
			SUM(CASE WHEN COALESCE(p.cnt, 0) <= 1 THEN 1 ELSE 0 END) AS dms,
			SUM(CASE WHEN COALESCE(p.cnt, 0) >= 2 THEN 1 ELSE 0 END) AS groups
		FROM chat c
		LEFT JOIN (
			SELECT chat_id, COUNT(*) AS cnt
			FROM chat_handle_join
			GROUP BY chat_id
		) p ON p.chat_id = c.ROWID
		WHERE EXISTS (
			SELECT 1
			FROM chat_message_join cmj
			JOIN message m ON m.ROWID = cmj.message_id
			WHERE cmj.chat_id = c.ROWID%s
		)
	`, tapbackCondition(includeTapbacks, " AND"))

	var dms, groups sql.NullInt64
	if err := db.QueryRow(q).Scan(&dms, &groups); err != nil {
		return c, fmt.Errorf("audit conversations: %w", err)
	}
	c.DMs = dms.Int64
	c.Groups = groups.Int64
	c.Unique = c.DMs + c.Groups
	return c, nil
}

// queryAuditLongestByCount returns the top N chats by non-tapback message count.
func queryAuditLongestByCount(db *sql.DB, topN int, includeTapbacks bool) ([]AuditChatStat, error) {
	q := fmt.Sprintf(`
		SELECT
			COALESCE(c.guid, ''),
			COALESCE(c.chat_identifier, ''),
			COALESCE(c.display_name, ''),
			COALESCE(p.cnt, 0),
			msg.cnt,
			msg.first_date,
			msg.last_date
		FROM chat c
		LEFT JOIN (
			SELECT chat_id, COUNT(*) AS cnt
			FROM chat_handle_join
			GROUP BY chat_id
		) p ON p.chat_id = c.ROWID
		JOIN (
			SELECT cmj.chat_id,
				COUNT(*) AS cnt,
				MIN(m.date) AS first_date,
				MAX(m.date) AS last_date
			FROM chat_message_join cmj
			JOIN message m ON m.ROWID = cmj.message_id
			WHERE 1 = 1%s
			GROUP BY cmj.chat_id
		) msg ON msg.chat_id = c.ROWID
		ORDER BY msg.cnt DESC
		LIMIT %d
	`, tapbackCondition(includeTapbacks, " AND"), topN)
	return scanAuditChats(db, q)
}

// queryAuditLongestBySpan returns the top N chats by the elapsed time
// between their first and last non-tapback message. Excludes chats with
// only one message (span = 0 days).
func queryAuditLongestBySpan(db *sql.DB, topN int, includeTapbacks bool) ([]AuditChatStat, error) {
	q := fmt.Sprintf(`
		SELECT
			COALESCE(c.guid, ''),
			COALESCE(c.chat_identifier, ''),
			COALESCE(c.display_name, ''),
			COALESCE(p.cnt, 0),
			msg.cnt,
			msg.first_date,
			msg.last_date
		FROM chat c
		LEFT JOIN (
			SELECT chat_id, COUNT(*) AS cnt
			FROM chat_handle_join
			GROUP BY chat_id
		) p ON p.chat_id = c.ROWID
		JOIN (
			SELECT cmj.chat_id,
				COUNT(*) AS cnt,
				MIN(m.date) AS first_date,
				MAX(m.date) AS last_date
			FROM chat_message_join cmj
			JOIN message m ON m.ROWID = cmj.message_id
			WHERE 1 = 1%s
			GROUP BY cmj.chat_id
			HAVING COUNT(*) >= 2
		) msg ON msg.chat_id = c.ROWID
		ORDER BY (msg.last_date - msg.first_date) DESC
		LIMIT %d
	`, tapbackCondition(includeTapbacks, " AND"), topN)
	return scanAuditChats(db, q)
}

func scanAuditChats(db *sql.DB, q string) ([]AuditChatStat, error) {
	rows, err := db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("audit longest: %w", err)
	}
	defer rows.Close()

	var out []AuditChatStat
	for rows.Next() {
		var s AuditChatStat
		var firstRaw, lastRaw sql.NullInt64
		if err := rows.Scan(&s.GUID, &s.ChatIdentifier, &s.DisplayName,
			&s.Participants, &s.MessageCount, &firstRaw, &lastRaw); err != nil {
			return nil, fmt.Errorf("scan audit chat: %w", err)
		}
		if s.Participants <= 1 {
			s.Kind = "dm"
		} else {
			s.Kind = "group"
		}
		if firstRaw.Valid {
			t := cocoaToUnix(firstRaw.Int64)
			s.FirstMessageDate = &t
		}
		if lastRaw.Valid {
			t := cocoaToUnix(lastRaw.Int64)
			s.LastMessageDate = &t
		}
		if s.FirstMessageDate != nil && s.LastMessageDate != nil {
			s.SpanDays = int(s.LastMessageDate.Sub(*s.FirstMessageDate).Hours() / 24)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// queryAuditActivity buckets chats by recency of their most recent message.
// All four buckets are disjoint-by-window: a chat active in the last 30 days
// also counts in the 90 and 365 buckets (nested totals).
func queryAuditActivity(db *sql.DB, includeTapbacks bool) (AuditActivity, error) {
	var a AuditActivity

	now := time.Now()
	d30 := cocoaSeconds(now.AddDate(0, 0, -30))
	d90 := cocoaSeconds(now.AddDate(0, 0, -90))
	d365 := cocoaSeconds(now.AddDate(0, 0, -365))

	// last_unix here is the unix epoch seconds of each chat's most-recent
	// real message, derived from chat.db's per-row Cocoa-nanos timestamps.
	q := fmt.Sprintf(`
		SELECT
			SUM(CASE WHEN last_unix >= ? THEN 1 ELSE 0 END) AS d30,
			SUM(CASE WHEN last_unix >= ? THEN 1 ELSE 0 END) AS d90,
			SUM(CASE WHEN last_unix >= ? THEN 1 ELSE 0 END) AS d365,
			SUM(CASE WHEN last_unix <  ? THEN 1 ELSE 0 END) AS dormant
		FROM (
			SELECT
				CASE WHEN MAX(m.date) >= %d
					 THEN MAX(m.date) / 1000000000 + %d
					 ELSE MAX(m.date) + %d
				END AS last_unix
			FROM chat_message_join cmj
			JOIN message m ON m.ROWID = cmj.message_id
			WHERE m.date > 0%s
			GROUP BY cmj.chat_id
		)
	`, nanosecondMagnitude, cocoaEpoch, cocoaEpoch, tapbackCondition(includeTapbacks, " AND"))

	// d365 in the SQL above is "active in 365d"; the dormant SUM uses the
	// same per-chat last_unix and counts those older than the 365-day cutoff.
	var d30c, d90c, d365c, dormant sql.NullInt64
	if err := db.QueryRow(q, d30, d90, d365, d365).Scan(&d30c, &d90c, &d365c, &dormant); err != nil {
		return a, fmt.Errorf("audit activity: %w", err)
	}
	a.Last30Days = d30c.Int64
	a.Last90Days = d90c.Int64
	a.Last365Days = d365c.Int64
	a.Dormant = dormant.Int64
	return a, nil
}

// queryAuditDistribution pulls per-chat non-tapback message counts and
// computes avg / median / p90 / max in process. Even a heavy user has at
// most a few thousand chats, so a single full pull is cheaper and simpler
// than running four separate percentile queries.
func queryAuditDistribution(db *sql.DB, includeTapbacks bool) (AuditDistribution, error) {
	var d AuditDistribution
	q := fmt.Sprintf(`
		SELECT COUNT(*) AS cnt
		FROM chat_message_join cmj
		JOIN message m ON m.ROWID = cmj.message_id
		WHERE 1 = 1%s
		GROUP BY cmj.chat_id
	`, tapbackCondition(includeTapbacks, " AND"))
	rows, err := db.Query(q)
	if err != nil {
		return d, fmt.Errorf("audit distribution: %w", err)
	}
	defer rows.Close()

	var counts []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return d, fmt.Errorf("scan distribution row: %w", err)
		}
		counts = append(counts, n)
	}
	if err := rows.Err(); err != nil {
		return d, err
	}
	if len(counts) == 0 {
		return d, nil
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i] < counts[j] })

	var sum int64
	for _, n := range counts {
		sum += n
	}
	d.AvgPerChat = float64(sum) / float64(len(counts))
	d.MedianPerChat = counts[len(counts)/2]
	p90Idx := (len(counts) * 9) / 10
	if p90Idx >= len(counts) {
		p90Idx = len(counts) - 1
	}
	d.P90PerChat = counts[p90Idx]
	d.MaxPerChat = counts[len(counts)-1]
	return d, nil
}

func queryAuditDirection(db *sql.DB, includeTapbacks bool) (AuditDirection, error) {
	var d AuditDirection
	q := fmt.Sprintf(`
		SELECT
			SUM(CASE WHEN m.is_from_me = 1 THEN 1 ELSE 0 END) AS me,
			SUM(CASE WHEN m.is_from_me = 0 THEN 1 ELSE 0 END) AS others
		FROM message m
		WHERE 1 = 1%s
	`, tapbackCondition(includeTapbacks, " AND"))
	var me, others sql.NullInt64
	if err := db.QueryRow(q).Scan(&me, &others); err != nil {
		return d, fmt.Errorf("audit direction: %w", err)
	}
	d.FromMe = me.Int64
	d.FromOthers = others.Int64
	total := d.FromMe + d.FromOthers
	if total > 0 {
		d.FromMePercent = float64(d.FromMe) / float64(total) * 100
	}
	return d, nil
}

// cocoaSeconds converts a time.Time to seconds since the Cocoa reference
// date (2001-01-01 UTC), which is the unit chat.db uses when its `date`
// column is in seconds rather than nanoseconds. The audit's activity query
// compares against this domain after dividing nanosecond timestamps by 1e9.
func cocoaSeconds(t time.Time) int64 { return t.Unix() - cocoaEpoch }

// ── table rendering ───────────────────────────────────────────────────────────

func printAuditTable(out io.Writer, f *rootFlags, r AuditResult) error {
	fmt.Fprintln(out, bold(f, out, "Conversations"))
	fmt.Fprintf(out, "  %d unique · %d DMs · %d groups\n", r.Conversations.Unique, r.Conversations.DMs, r.Conversations.Groups)
	fmt.Fprintln(out)

	if len(r.LongestByCount) > 0 {
		fmt.Fprintln(out, bold(f, out, "Longest by message count"))
		w := newTabWriter(out)
		fmt.Fprintln(w, "  "+bold(f, out, "Chat")+"\t"+bold(f, out, "Kind")+"\t"+bold(f, out, "Messages")+"\t"+bold(f, out, "Span"))
		for _, c := range r.LongestByCount {
			fmt.Fprintf(w, "  %s\t%s\t%d\t%s\n", auditChatLabel(c), c.Kind, c.MessageCount, auditSpan(c))
		}
		if err := w.Flush(); err != nil {
			return err
		}
		fmt.Fprintln(out)
	}

	if len(r.LongestBySpan) > 0 {
		fmt.Fprintln(out, bold(f, out, "Longest by date span"))
		w := newTabWriter(out)
		fmt.Fprintln(w, "  "+bold(f, out, "Chat")+"\t"+bold(f, out, "Kind")+"\t"+bold(f, out, "Span")+"\t"+bold(f, out, "Messages"))
		for _, c := range r.LongestBySpan {
			fmt.Fprintf(w, "  %s\t%s\t%s\t%d\n", auditChatLabel(c), c.Kind, auditSpan(c), c.MessageCount)
		}
		if err := w.Flush(); err != nil {
			return err
		}
		fmt.Fprintln(out)
	}

	fmt.Fprintln(out, bold(f, out, "Activity"))
	fmt.Fprintf(out, "  Last  30 days: %d chats\n", r.Activity.Last30Days)
	fmt.Fprintf(out, "  Last  90 days: %d chats\n", r.Activity.Last90Days)
	fmt.Fprintf(out, "  Last 365 days: %d chats\n", r.Activity.Last365Days)
	fmt.Fprintf(out, "  Dormant (no msg in 365d+): %d chats\n", r.Activity.Dormant)
	fmt.Fprintln(out)

	fmt.Fprintln(out, bold(f, out, "Messages per chat"))
	fmt.Fprintf(out, "  avg: %.1f · median: %d · p90: %d · max: %d\n",
		r.MessageDistribution.AvgPerChat,
		r.MessageDistribution.MedianPerChat,
		r.MessageDistribution.P90PerChat,
		r.MessageDistribution.MaxPerChat,
	)
	fmt.Fprintln(out)

	fmt.Fprintln(out, bold(f, out, "Direction"))
	fmt.Fprintf(out, "  from me:     %d (%.1f%%)\n", r.Direction.FromMe, r.Direction.FromMePercent)
	fmt.Fprintf(out, "  from others: %d (%.1f%%)\n", r.Direction.FromOthers, 100-r.Direction.FromMePercent)

	return nil
}

// auditChatLabel picks the most-human-friendly identifier we have: display
// name if set (group chats often have one), otherwise the chat_identifier
// (phone or email for DMs), and finally the GUID as a last resort.
func auditChatLabel(c AuditChatStat) string {
	if c.DisplayName != "" {
		return c.DisplayName
	}
	if c.ChatIdentifier != "" {
		return c.ChatIdentifier
	}
	return c.GUID
}

func auditSpan(c AuditChatStat) string {
	if c.SpanDays >= 365 {
		years := float64(c.SpanDays) / 365.0
		return fmt.Sprintf("%.1f yrs", years)
	}
	if c.SpanDays >= 30 {
		months := float64(c.SpanDays) / 30.0
		return fmt.Sprintf("%.1f mos", months)
	}
	return fmt.Sprintf("%d days", c.SpanDays)
}
