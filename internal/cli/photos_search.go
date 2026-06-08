// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — photos_search.go
// `photos search` filters the Photos library by person, date, type, favorite
// status, and location — all read directly from Photos.sqlite with no Photos.app
// launch. Output is the same Asset shape as `photos top`, so UUIDs pipe straight
// into `photos download` and `photos delete`.
package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newSearchCmd(f *rootFlags) *cobra.Command {
	var opts PhotoSearchOpts
	var nearStr string

	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search the library by person, date, type, favorite, or location",
		Long: `Search Photos.sqlite directly with any combination of filters:

  --person NAME      photos with a named person from your People album
  --year / --month   restrict to a year (and optionally month)
  --type             all | photo | video
  --favorites        only favorited items
  --has-gps          only items with location data
  --near LAT,LON     items within --radius km of a coordinate
  --keyword TEXT     match the original filename

Filters combine with AND. Results print newest-first and carry the UUID,
so you can pipe straight into download or delete:

  icloud-pp-cli photos search --person "Mom" --year 2024 --json \
    | jq -r '.[].uuid' | xargs icloud-pp-cli photos download --output ~/Desktop/mom`,
		Example: `  icloud-pp-cli photos search --person "Mom" --year 2023
  icloud-pp-cli photos search --favorites --type photo --limit 50
  icloud-pp-cli photos search --near 25.79,-80.13 --radius 5
  icloud-pp-cli photos search --keyword IMG_1234 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()

			if opts.MediaType != "all" && opts.MediaType != "photo" && opts.MediaType != "video" {
				return usageErr(fmt.Errorf("--type must be one of: all, photo, video"))
			}
			if opts.Month > 0 && opts.Year == 0 {
				return usageErr(fmt.Errorf("--month requires --year"))
			}
			if opts.Month < 0 || opts.Month > 12 {
				return usageErr(fmt.Errorf("--month must be between 1 and 12"))
			}
			if nearStr != "" {
				lat, lon, err := parseLatLon(nearStr)
				if err != nil {
					return usageErr(err)
				}
				opts.NearLat, opts.NearLon = lat, lon
				if opts.RadiusKM <= 0 {
					opts.RadiusKM = 5 // sensible default radius
				}
			}
			if opts.RadiusKM > 0 && nearStr == "" {
				return usageErr(fmt.Errorf("--radius requires --near LAT,LON"))
			}
			if !anyFilterSet(opts, nearStr) {
				return usageErr(fmt.Errorf("provide at least one filter (e.g. --person, --year, --favorites, --near, --keyword)"))
			}

			db, err := openPhotosDB(f.libraryPath)
			if err != nil {
				return configErr(err)
			}
			defer db.Close()

			assets, err := queryPhotosSearch(db, opts)
			if err != nil {
				return fmt.Errorf("search failed: %w", err)
			}
			if len(assets) == 0 {
				fmt.Fprintln(out, "No matching photos found.")
				return nil
			}

			if f.asJSON || !isTerminal(out) {
				return printTopJSON(cmd, f, assets)
			}
			if err := printTopTable(cmd, f, assets); err != nil {
				return err
			}
			fmt.Fprintf(out, "\n%s matching items\n", formatInt(int64(len(assets))))
			return nil
		},
	}

	cmd.Flags().StringVar(&opts.MediaType, "type", "all", "Media type: all, photo, video")
	cmd.Flags().IntVar(&opts.Year, "year", 0, "Restrict to a year (e.g. 2024)")
	cmd.Flags().IntVar(&opts.Month, "month", 0, "Restrict to a month 1-12 (requires --year)")
	cmd.Flags().StringVar(&opts.Person, "person", "", "Match a named person from your People album")
	cmd.Flags().StringVar(&opts.Keyword, "keyword", "", "Match the original filename")
	cmd.Flags().BoolVar(&opts.Favorites, "favorites", false, "Only favorited items")
	cmd.Flags().BoolVar(&opts.HasGPS, "has-gps", false, "Only items with location data")
	cmd.Flags().StringVar(&nearStr, "near", "", "Center coordinate as LAT,LON (e.g. 25.79,-80.13)")
	cmd.Flags().Float64Var(&opts.RadiusKM, "radius", 0, "Radius in km around --near (default 5 when --near is set)")
	cmd.Flags().IntVar(&opts.Limit, "limit", 100, "Maximum results (0 = all)")

	return cmd
}

// parseLatLon parses a "LAT,LON" string into two float64 coordinates.
func parseLatLon(s string) (lat, lon float64, err error) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("--near must be LAT,LON (e.g. 25.79,-80.13)")
	}
	lat, err = strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("--near latitude: %v", err)
	}
	lon, err = strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("--near longitude: %v", err)
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return 0, 0, fmt.Errorf("--near coordinates out of range")
	}
	return lat, lon, nil
}

// anyFilterSet reports whether the user supplied at least one real filter, so a
// bare `photos search` errors instead of dumping the whole library.
func anyFilterSet(opts PhotoSearchOpts, nearStr string) bool {
	return opts.Person != "" || opts.Year != 0 || opts.Keyword != "" ||
		opts.Favorites || opts.HasGPS || nearStr != "" ||
		opts.MediaType == "photo" || opts.MediaType == "video"
}
