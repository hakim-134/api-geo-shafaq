package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// moderate is the admin's side of reports (PLAN 3.7): see what players reported, ban or
// unban a player. A ban stops new games and follows the player into any account merged
// with theirs; their games stay.
func moderate(ctx context.Context, q *store.Queries, cmd string, args []string) error {
	if cmd == "reports" {
		rows, err := q.OpenReports(ctx)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Println("No open reports.")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "PLAYER\tNAME NOW\tREPORTED AS\tREASON\tBY\tLAST\tBANNED")
		for _, r := range rows {
			banned := ""
			if r.BannedAt != nil {
				banned = "yes"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", r.UserID, r.CurrentName, r.Name, r.Reason,
				r.Reporters, r.LastAt.Format("2006-01-02 15:04"), banned)
		}
		return tw.Flush()
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: shafaq-tools %s <player id or name> [reason]", cmd)
	}
	users, err := q.FindUsers(ctx, args[0])
	if err != nil {
		return err
	}
	switch {
	case len(users) == 0:
		return errors.New("no such player")
	case len(users) > 1:
		for _, u := range users {
			fmt.Printf("%s  %s\n", u.ID, u.Name)
		}
		return errors.New("several players match: use the id")
	}
	u := users[0]
	if cmd == "unban" {
		if _, err := q.UnbanUser(ctx, u.ID); err != nil {
			return err
		}
		fmt.Printf("Unbanned %s (%s).\n", u.Name, u.ID)
		return nil
	}
	if _, err := q.BanUser(ctx, store.BanUserParams{ID: u.ID, Reason: strings.Join(args[1:], " ")}); err != nil {
		return err
	}
	n, err := q.ResolveReports(ctx, u.ID)
	if err != nil {
		return err
	}
	fmt.Printf("Banned %s (%s); %d report(s) closed.\n", u.Name, u.ID, n)
	return nil
}

// moderateMaps is the queue of public maps (PLAN 8.2, the first part of 6.4): what waits, then
// approve it into the search or reject it with a reason its owner sees. Until then a map plays by
// its link. The API reads this state fresh: it takes effect at once.
func moderateMaps(ctx context.Context, q *store.Queries, cmd string, args []string) error {
	if cmd == "queue" {
		rows, err := q.ModerationQueue(ctx)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Println("No maps wait.")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "MAP\tNAME\tBY\tTAGS\tSOURCES\tWAITS SINCE")
		for _, r := range rows {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", r.Slug, r.NameUz, r.OwnerName, strings.Join(r.Tags, ","),
				r.Sources, r.UpdatedAt.Format("2006-01-02 15:04"))
		}
		return tw.Flush()
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: shafaq-tools %s <map> [reason]", cmd)
	}
	state := "approved"
	if cmd == "reject" {
		state = "rejected"
	}
	n, err := q.ModerateMap(ctx, store.ModerateMapParams{Slug: args[0], Moderation: state, ModerationNote: strings.Join(args[1:], " ")})
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("no such map waits")
	}
	fmt.Printf("%s: %s.\n", args[0], state)
	return nil
}
