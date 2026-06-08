// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — mail.go
// `mail` command group: read the macOS Mail "Envelope Index" — message metadata
// across all accounts (subjects, senders, dates, read/flag state, mailboxes).
// Read-only. It indexes message envelopes, not bodies. FDA-gated.
package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
)

func newMailCmd(f *rootFlags) *cobra.Command {
	mail := &cobra.Command{
		Use:   "mail",
		Short: "Read your Mail index — messages, senders, mailboxes",
		Long: `Read the macOS Mail "Envelope Index" — message metadata across every
account: subjects, senders, dates, read/flagged state, and mailbox counts.
This indexes envelopes (headers), not message bodies. Read-only.

Requires Full Disk Access (run "icloud-pp-cli doctor" if denied).`,
	}
	mail.AddCommand(newMailListCmd(f))
	mail.AddCommand(newMailSearchCmd(f))
	mail.AddCommand(newMailMailboxesCmd(f))
	mail.AddCommand(newMailAnalyticsCmd(f))
	return mail
}

func newMailListCmd(f *rootFlags) *cobra.Command {
	var filter MailFilter
	var sinceStr string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recent messages (newest first)",
		Example: `  icloud-pp-cli mail list
  icloud-pp-cli mail list --unread
  icloud-pp-cli mail list --from amazon --limit 30
  icloud-pp-cli mail list --flagged`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if sinceStr != "" {
				t, err := time.ParseInLocation("2006-01-02", sinceStr, time.Local)
				if err != nil {
					return usageErr(fmt.Errorf("--since: expected YYYY-MM-DD, got %q", sinceStr))
				}
				filter.Since = t
			}
			db, err := openMailIndex("")
			if err != nil {
				return err
			}
			defer db.Close()
			mc, err := detectMailColumns(db)
			if err != nil {
				return err
			}
			msgs, err := queryMail(db, mc, filter)
			if err != nil {
				return err
			}
			if len(msgs) == 0 {
				fmt.Fprintln(out, "No matching messages.")
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, msgs)
			}
			printMailTable(f, out, msgs)
			fmt.Fprintf(out, "\n%s messages\n", formatInt(int64(len(msgs))))
			return nil
		},
	}
	cmd.Flags().BoolVar(&filter.UnreadOnly, "unread", false, "Only unread messages")
	cmd.Flags().BoolVar(&filter.FlaggedOnly, "flagged", false, "Only flagged messages")
	cmd.Flags().StringVar(&filter.From, "from", "", "Filter by sender address or name (substring)")
	cmd.Flags().StringVar(&sinceStr, "since", "", "Only messages on or after this date (YYYY-MM-DD)")
	cmd.Flags().IntVar(&filter.Limit, "limit", 100, "Maximum messages")
	return cmd
}

func newMailSearchCmd(f *rootFlags) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:     "search <query>",
		Short:   "Search messages by subject or sender",
		Args:    cobra.MinimumNArgs(1),
		Example: `  icloud-pp-cli mail search invoice`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			db, err := openMailIndex("")
			if err != nil {
				return err
			}
			defer db.Close()
			mc, err := detectMailColumns(db)
			if err != nil {
				return err
			}
			query := joinArgs(args)
			msgs, err := searchMail(db, mc, query, limit)
			if err != nil {
				return err
			}
			if len(msgs) == 0 {
				fmt.Fprintf(out, "No messages match %q.\n", query)
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, msgs)
			}
			printMailTable(f, out, msgs)
			fmt.Fprintf(out, "\n%s matches for %q\n", formatInt(int64(len(msgs))), query)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum results")
	return cmd
}

func newMailMailboxesCmd(f *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mailboxes",
		Aliases: []string{"boxes"},
		Short:   "List mailboxes with total and unread counts",
		Example: `  icloud-pp-cli mail mailboxes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			db, err := openMailIndex("")
			if err != nil {
				return err
			}
			defer db.Close()
			boxes, err := queryMailboxes(db)
			if err != nil {
				return err
			}
			if len(boxes) == 0 {
				fmt.Fprintln(out, "No mailboxes found.")
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, boxes)
			}
			tw := newTabWriter(out)
			fmt.Fprintln(tw, bold(f, out, "Mailbox")+"\t"+bold(f, out, "Total")+"\t"+bold(f, out, "Unread"))
			for _, mb := range boxes {
				fmt.Fprintf(tw, "%s\t%d\t%d\n", truncate(mb.Name, 36), mb.Total, mb.Unread)
			}
			tw.Flush()
			return nil
		},
	}
	return cmd
}

func newMailAnalyticsCmd(f *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "analytics",
		Short:   "Mail overview + top senders",
		Example: `  icloud-pp-cli mail analytics`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			db, err := openMailIndex("")
			if err != nil {
				return err
			}
			defer db.Close()
			mc, err := detectMailColumns(db)
			if err != nil {
				return err
			}
			ov, err := mailOverview(db, mc)
			if err != nil {
				return err
			}
			senders, err := topMailSenders(db, 15)
			if err != nil {
				return err
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, struct {
					Overview   *MailOverview    `json:"overview"`
					TopSenders []MailSenderStat `json:"top_senders"`
				}{ov, senders})
			}
			fmt.Fprintln(out, bold(f, out, "Mail overview"))
			fmt.Fprintf(out, "  %s messages · %s unread · %s flagged · %s senders\n",
				formatInt(ov.Total), formatInt(ov.Unread), formatInt(ov.Flagged), formatInt(ov.Senders))
			fmt.Fprintln(out)
			if len(senders) > 0 {
				fmt.Fprintln(out, bold(f, out, "Top senders"))
				tw := newTabWriter(out)
				fmt.Fprintln(tw, bold(f, out, "Sender")+"\t"+bold(f, out, "Messages"))
				for _, s := range senders {
					who := s.Name
					if who == "" {
						who = s.Address
					}
					fmt.Fprintf(tw, "%s\t%d\n", truncate(who, 40), s.Count)
				}
				tw.Flush()
			}
			return nil
		},
	}
	return cmd
}

func printMailTable(f *rootFlags, out io.Writer, msgs []MailMessage) {
	tw := newTabWriter(out)
	fmt.Fprintln(tw, bold(f, out, "Date")+"\t"+bold(f, out, "")+"\t"+bold(f, out, "From")+"\t"+bold(f, out, "Subject"))
	for _, m := range msgs {
		mark := " "
		if !m.Read {
			mark = green(f, out, "●")
		}
		if m.Flagged {
			mark += yellow(f, out, "⚑")
		}
		from := m.FromName
		if from == "" {
			from = m.FromAddress
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			shortDate(m.Date), mark, truncate(from, 24), truncate(m.Subject, 40))
	}
	tw.Flush()
}
