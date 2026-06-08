// Copyright 2026 mvanhorn. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"database/sql"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// CoreData timestamps count seconds since Jan 1 2001 00:00:00 UTC.
const coreDataEpoch int64 = 978307200

// Asset is one row from ZASSET joined with ZADDITIONALASSETATTRIBUTES.
type Asset struct {
	UUID      string
	Filename  string
	SizeBytes int64
	Kind      int // 0=image, 1=video
	Date      time.Time
}

func (a Asset) IsVideo() bool   { return a.Kind == 1 }
func (a Asset) SizeGB() float64 { return float64(a.SizeBytes) / (1 << 30) }
func (a Asset) SizeMB() float64 { return float64(a.SizeBytes) / (1 << 20) }

func (a Asset) TypeLabel() string {
	if a.IsVideo() {
		return "video"
	}
	return "photo"
}

// StorageRow summarises assets grouped by an arbitrary label.
type StorageRow struct {
	Label     string `json:"label"`
	Count     int64  `json:"count"`
	SizeBytes int64  `json:"size_bytes"`
}

func (r StorageRow) SizeGB() float64 { return float64(r.SizeBytes) / (1 << 30) }

func defaultLibraryPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Pictures", "Photos Library.photoslibrary", "database", "Photos.sqlite")
}

func openPhotosDB(libraryPath string) (*sql.DB, error) {
	if runtime.GOOS != "darwin" {
		return nil, configErr(fmt.Errorf(
			"icloud-pp-cli requires macOS — this is %s", runtime.GOOS,
		))
	}
	if libraryPath == "" {
		libraryPath = defaultLibraryPath()
	}
	if _, err := os.Stat(libraryPath); err != nil {
		return nil, fmt.Errorf(
			"Photos library not found at %s\n\nUse --library to specify a custom path.",
			libraryPath,
		)
	}
	u := &url.URL{
		Scheme:   "file",
		Path:     libraryPath,
		RawQuery: "mode=ro&_busy_timeout=5000",
	}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("cannot open Photos library: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot read Photos library: %w\n\nTry quitting Photos.app and running again.", err)
	}
	return db, nil
}

// queryLargestVideos returns up to limit videos sorted largest-first.
// File sizes and original filenames live in ZADDITIONALASSETATTRIBUTES.
func queryLargestVideos(db *sql.DB, limit int, year, month int) ([]Asset, error) {
	q := `
		SELECT
			COALESCE(a.ZUUID, ''),
			COALESCE(aa.ZORIGINALFILENAME, a.ZFILENAME, ''),
			COALESCE(aa.ZORIGINALFILESIZE, 0),
			COALESCE(a.ZKIND, 0),
			COALESCE(a.ZDATECREATED, 0)
		FROM ZASSET a
		JOIN ZADDITIONALASSETATTRIBUTES aa ON aa.ZASSET = a.Z_PK
		WHERE a.ZKIND = 1
		  AND a.ZTRASHEDSTATE = 0
		  AND aa.ZORIGINALFILESIZE > 0
	`
	args := []any{}
	if year > 0 {
		q += " AND CAST(strftime('%Y', datetime(a.ZDATECREATED + 978307200, 'unixepoch')) AS INTEGER) = ?"
		args = append(args, year)
	}
	if month > 0 {
		q += " AND CAST(strftime('%m', datetime(a.ZDATECREATED + 978307200, 'unixepoch')) AS INTEGER) = ?"
		args = append(args, month)
	}
	q += " ORDER BY aa.ZORIGINALFILESIZE DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}

	return scanAssets(db, q, args...)
}

// queryStorageByType returns totals grouped by ZKIND (photo vs video).
func queryStorageByType(db *sql.DB) ([]StorageRow, error) {
	q := `
		SELECT
			CASE a.ZKIND WHEN 1 THEN 'video' ELSE 'photo' END,
			COUNT(*),
			SUM(COALESCE(aa.ZORIGINALFILESIZE, 0))
		FROM ZASSET a
		JOIN ZADDITIONALASSETATTRIBUTES aa ON aa.ZASSET = a.Z_PK
		WHERE a.ZTRASHEDSTATE = 0
		GROUP BY a.ZKIND
		ORDER BY SUM(COALESCE(aa.ZORIGINALFILESIZE, 0)) DESC
	`
	return scanStorageRows(db, q)
}

// queryStorageByYear returns totals grouped by year.
func queryStorageByYear(db *sql.DB) ([]StorageRow, error) {
	q := `
		SELECT
			strftime('%Y', datetime(a.ZDATECREATED + 978307200, 'unixepoch')),
			COUNT(*),
			SUM(COALESCE(aa.ZORIGINALFILESIZE, 0))
		FROM ZASSET a
		JOIN ZADDITIONALASSETATTRIBUTES aa ON aa.ZASSET = a.Z_PK
		WHERE a.ZTRASHEDSTATE = 0
		GROUP BY 1
		ORDER BY 1 DESC
	`
	return scanStorageRows(db, q)
}

// queryTopFiles returns the heaviest files across all (or a filtered) media type.
func queryTopFiles(db *sql.DB, limit int, mediaType string) ([]Asset, error) {
	kindFilter := ""
	switch mediaType {
	case "video":
		kindFilter = "AND a.ZKIND = 1"
	case "photo":
		kindFilter = "AND a.ZKIND = 0"
	}

	q := fmt.Sprintf(`
		SELECT
			COALESCE(a.ZUUID, ''),
			COALESCE(aa.ZORIGINALFILENAME, a.ZFILENAME, ''),
			COALESCE(aa.ZORIGINALFILESIZE, 0),
			COALESCE(a.ZKIND, 0),
			COALESCE(a.ZDATECREATED, 0)
		FROM ZASSET a
		JOIN ZADDITIONALASSETATTRIBUTES aa ON aa.ZASSET = a.Z_PK
		WHERE a.ZTRASHEDSTATE = 0
		  AND aa.ZORIGINALFILESIZE > 0
		  %s
		ORDER BY aa.ZORIGINALFILESIZE DESC
	`, kindFilter)

	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	return scanAssets(db, q)
}

// querySensitiveAssets returns assets Apple's on-device ML has flagged as containing
// nudity (ZSCREENTIMEDEVICEIMAGESENSITIVITY = 1). Results are shuffled randomly so
// repeated calls with the same limit produce a varied sample. Pass limit=0 for all.
func querySensitiveAssets(db *sql.DB, limit int, mediaType string) ([]Asset, error) {
	kindFilter := ""
	switch mediaType {
	case "video":
		kindFilter = "AND a.ZKIND = 1"
	case "photo":
		kindFilter = "AND a.ZKIND = 0"
	}

	q := fmt.Sprintf(`
		SELECT
			COALESCE(a.ZUUID, ''),
			COALESCE(aa.ZORIGINALFILENAME, a.ZFILENAME, ''),
			COALESCE(aa.ZORIGINALFILESIZE, 0),
			COALESCE(a.ZKIND, 0),
			COALESCE(a.ZDATECREATED, 0)
		FROM ZASSET a
		JOIN ZADDITIONALASSETATTRIBUTES aa ON aa.ZASSET = a.Z_PK
		JOIN ZMEDIAANALYSISASSETATTRIBUTES m ON m.ZASSET = a.Z_PK
		WHERE a.ZTRASHEDSTATE = 0
		  AND m.ZSCREENTIMEDEVICEIMAGESENSITIVITY = 1
		  %s
		ORDER BY RANDOM()
	`, kindFilter)
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	return scanAssets(db, q)
}

// queryByUUIDs returns assets matching the given UUIDs (used by the delete command).
// Batches requests in chunks of 999 to stay within SQLite's SQLITE_LIMIT_VARIABLE_NUMBER.
func queryByUUIDs(db *sql.DB, uuids []string) ([]Asset, error) {
	if len(uuids) == 0 {
		return nil, nil
	}
	const batchSize = 999
	var out []Asset
	for i := 0; i < len(uuids); i += batchSize {
		end := i + batchSize
		if end > len(uuids) {
			end = len(uuids)
		}
		batch := uuids[i:end]
		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for j, u := range batch {
			placeholders[j] = "?"
			args[j] = u
		}
		q := fmt.Sprintf(`
			SELECT
				COALESCE(a.ZUUID, ''),
				COALESCE(aa.ZORIGINALFILENAME, a.ZFILENAME, ''),
				COALESCE(aa.ZORIGINALFILESIZE, 0),
				COALESCE(a.ZKIND, 0),
				COALESCE(a.ZDATECREATED, 0)
			FROM ZASSET a
			JOIN ZADDITIONALASSETATTRIBUTES aa ON aa.ZASSET = a.Z_PK
			WHERE a.ZUUID IN (%s)
			  AND a.ZTRASHEDSTATE = 0
		`, strings.Join(placeholders, ","))
		assets, err := scanAssets(db, q, args...)
		if err != nil {
			return nil, err
		}
		out = append(out, assets...)
	}
	return out, nil
}

// queryTotals returns a single summary row across all non-trashed assets.
func queryTotals(db *sql.DB) (count int64, sizeBytes int64, err error) {
	row := db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(COALESCE(aa.ZORIGINALFILESIZE, 0)), 0)
		FROM ZASSET a
		JOIN ZADDITIONALASSETATTRIBUTES aa ON aa.ZASSET = a.Z_PK
		WHERE a.ZTRASHEDSTATE = 0
	`)
	err = row.Scan(&count, &sizeBytes)
	return
}

// ── search ──────────────────────────────────────────────────────────────────

// PhotoSearchOpts narrows a photo search. Zero-value fields are ignored, so any
// combination of filters can be ANDed together.
type PhotoSearchOpts struct {
	MediaType string // all | photo | video
	Year      int    // 0 = any
	Month     int    // 0 = any (requires Year)
	Person    string // substring match on detected-face person names
	Keyword   string // substring match on original filename
	Favorites bool   // only ZFAVORITE = 1
	HasGPS    bool   // only assets with valid GPS coordinates
	NearLat   float64
	NearLon   float64
	RadiusKM  float64 // > 0 enables a bounding-box filter around Near{Lat,Lon}
	Limit     int
}

// queryPhotosSearch runs a multi-filter search over ZASSET. Person filtering
// joins ZDETECTEDFACE → ZPERSON using runtime-detected column names so it works
// across the schema variations Apple has shipped over macOS versions.
func queryPhotosSearch(db *sql.DB, opts PhotoSearchOpts) ([]Asset, error) {
	var joins []string
	var conds []string
	var args []any

	conds = append(conds, "a.ZTRASHEDSTATE = 0")

	switch opts.MediaType {
	case "video":
		conds = append(conds, "a.ZKIND = 1")
	case "photo":
		conds = append(conds, "a.ZKIND = 0")
	}

	if opts.Year > 0 {
		startMonth, endMonth := 1, 13
		startYear, endYear := opts.Year, opts.Year
		if opts.Month > 0 {
			startMonth = opts.Month
			endMonth = opts.Month + 1
			if endMonth > 12 {
				endMonth = 1
				endYear = opts.Year + 1
			}
		}
		start := time.Date(startYear, time.Month(startMonth), 1, 0, 0, 0, 0, time.Local).Unix() - coreDataEpoch
		var end int64
		if opts.Month > 0 {
			end = time.Date(endYear, time.Month(endMonth), 1, 0, 0, 0, 0, time.Local).Unix() - coreDataEpoch
		} else {
			end = time.Date(opts.Year+1, 1, 1, 0, 0, 0, 0, time.Local).Unix() - coreDataEpoch
		}
		conds = append(conds, "a.ZDATECREATED >= ? AND a.ZDATECREATED < ?")
		args = append(args, start, end)
	}

	if opts.Keyword != "" {
		conds = append(conds, "(LOWER(aa.ZORIGINALFILENAME) LIKE ? OR LOWER(a.ZFILENAME) LIKE ?)")
		kw := "%" + strings.ToLower(opts.Keyword) + "%"
		args = append(args, kw, kw)
	}

	if opts.Favorites {
		conds = append(conds, "a.ZFAVORITE = 1")
	}

	if opts.HasGPS || opts.RadiusKM > 0 {
		// Filter out the "no GPS" sentinel (-180) and impossible coordinates.
		conds = append(conds, "a.ZLATITUDE BETWEEN -89 AND 89 AND a.ZLONGITUDE BETWEEN -179 AND 179")
	}

	if opts.RadiusKM > 0 {
		// Approximate bounding box: 1° latitude ≈ 111 km; longitude shrinks by
		// cos(latitude). Good enough for a search filter (not great-circle exact).
		dLat := opts.RadiusKM / 111.0
		cosLat := math.Cos(opts.NearLat * math.Pi / 180.0)
		if cosLat < 0.01 {
			cosLat = 0.01
		}
		dLon := opts.RadiusKM / (111.0 * cosLat)
		conds = append(conds, "a.ZLATITUDE BETWEEN ? AND ? AND a.ZLONGITUDE BETWEEN ? AND ?")
		args = append(args, opts.NearLat-dLat, opts.NearLat+dLat, opts.NearLon-dLon, opts.NearLon+dLon)
	}

	if opts.Person != "" {
		assetCol, personCol, nameCols, err := detectFaceColumns(db)
		if err != nil {
			return nil, err
		}
		joins = append(joins,
			fmt.Sprintf("JOIN ZDETECTEDFACE df ON df.%s = a.Z_PK", assetCol),
			fmt.Sprintf("JOIN ZPERSON p ON p.Z_PK = df.%s", personCol),
		)
		var nameConds []string
		like := "%" + strings.ToLower(opts.Person) + "%"
		for _, nc := range nameCols {
			nameConds = append(nameConds, fmt.Sprintf("LOWER(p.%s) LIKE ?", nc))
			args = append(args, like)
		}
		conds = append(conds, "("+strings.Join(nameConds, " OR ")+")")
	}

	q := fmt.Sprintf(`
		SELECT DISTINCT
			COALESCE(a.ZUUID, ''),
			COALESCE(aa.ZORIGINALFILENAME, a.ZFILENAME, ''),
			COALESCE(aa.ZORIGINALFILESIZE, 0),
			COALESCE(a.ZKIND, 0),
			COALESCE(a.ZDATECREATED, 0)
		FROM ZASSET a
		JOIN ZADDITIONALASSETATTRIBUTES aa ON aa.ZASSET = a.Z_PK
		%s
		WHERE %s
		ORDER BY a.ZDATECREATED DESC
	`, strings.Join(joins, "\n\t\t"), strings.Join(conds, "\n\t\t  AND "))

	if opts.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", opts.Limit)
	}
	return scanAssets(db, q, args...)
}

// detectFaceColumns inspects ZDETECTEDFACE and ZPERSON to find the columns that
// link a face to its asset and person, plus the person name column(s). Apple has
// used ZASSET/ZASSETFORFACE and ZPERSON/ZPERSONFORFACE across versions, so we
// resolve them at runtime rather than hard-coding one variant.
func detectFaceColumns(db *sql.DB) (assetCol, personCol string, nameCols []string, err error) {
	faceCols, err := tableColumns(db, "ZDETECTEDFACE")
	if err != nil || len(faceCols) == 0 {
		return "", "", nil, fmt.Errorf("person search unavailable — this Photos library has no face data (ZDETECTEDFACE)")
	}
	personCols, err := tableColumns(db, "ZPERSON")
	if err != nil || len(personCols) == 0 {
		return "", "", nil, fmt.Errorf("person search unavailable — this Photos library has no people (ZPERSON)")
	}

	assetCol = firstPresent(faceCols, "ZASSETFORFACE", "ZASSET")
	personCol = firstPresent(faceCols, "ZPERSONFORFACE", "ZPERSON")
	if assetCol == "" || personCol == "" {
		return "", "", nil, fmt.Errorf("person search unavailable — unrecognized ZDETECTEDFACE schema")
	}
	for _, c := range []string{"ZFULLNAME", "ZDISPLAYNAME", "ZNAME"} {
		if _, ok := personCols[c]; ok {
			nameCols = append(nameCols, c)
		}
	}
	if len(nameCols) == 0 {
		return "", "", nil, fmt.Errorf("person search unavailable — no name column on ZPERSON")
	}
	return assetCol, personCol, nameCols, nil
}

// tableColumns returns the set of column names for a table via PRAGMA table_info.
func tableColumns(db *sql.DB, table string) (map[string]struct{}, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = struct{}{}
	}
	return cols, rows.Err()
}

// firstPresent returns the first candidate found in the column set, or "".
func firstPresent(cols map[string]struct{}, candidates ...string) string {
	for _, c := range candidates {
		if _, ok := cols[c]; ok {
			return c
		}
	}
	return ""
}

// ── internal helpers ──────────────────────────────────────────────────────────

func scanAssets(db *sql.DB, q string, args ...any) ([]Asset, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Asset
	for rows.Next() {
		var a Asset
		var created float64
		if err := rows.Scan(&a.UUID, &a.Filename, &a.SizeBytes, &a.Kind, &created); err != nil {
			continue
		}
		a.Date = time.Unix(int64(created)+coreDataEpoch, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanStorageRows(db *sql.DB, q string, args ...any) ([]StorageRow, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StorageRow
	for rows.Next() {
		var r StorageRow
		if err := rows.Scan(&r.Label, &r.Count, &r.SizeBytes); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
