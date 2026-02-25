// nfl_teams.go — NFL team seed data for the sports service.
// P15-T03: Seeds all 32 NFL teams if the table is empty for the NFL league.
package data

import (
	"context"
	"database/sql"
	"log"
)

type nflTeam struct {
	name           string
	shortName      string
	abbreviation   string
	city           string
	venue          string
	primaryColor   string
	secondaryColor string
	conference     string
	division       string
}

// nflTeams contains all 32 NFL teams across 8 divisions.
var nflTeams = []nflTeam{
	// NFC East
	{"Philadelphia Eagles", "Eagles", "PHI", "Philadelphia", "Lincoln Financial Field", "#004C54", "#A5ACAF", "NFC", "NFC East"},
	{"Dallas Cowboys", "Cowboys", "DAL", "Dallas", "AT&T Stadium", "#003594", "#869397", "NFC", "NFC East"},
	{"New York Giants", "Giants", "NYG", "East Rutherford", "MetLife Stadium", "#0B2265", "#A71930", "NFC", "NFC East"},
	{"Washington Commanders", "Commanders", "WAS", "Landover", "Northwest Stadium", "#5A1414", "#FFB612", "NFC", "NFC East"},
	// NFC North
	{"Chicago Bears", "Bears", "CHI", "Chicago", "Soldier Field", "#0B162A", "#C83803", "NFC", "NFC North"},
	{"Detroit Lions", "Lions", "DET", "Detroit", "Ford Field", "#0076B6", "#B0B7BC", "NFC", "NFC North"},
	{"Green Bay Packers", "Packers", "GB", "Green Bay", "Lambeau Field", "#203731", "#FFB612", "NFC", "NFC North"},
	{"Minnesota Vikings", "Vikings", "MIN", "Minneapolis", "US Bank Stadium", "#4F2683", "#FFC62F", "NFC", "NFC North"},
	// NFC South
	{"Atlanta Falcons", "Falcons", "ATL", "Atlanta", "Mercedes-Benz Stadium", "#A71930", "#000000", "NFC", "NFC South"},
	{"Carolina Panthers", "Panthers", "CAR", "Charlotte", "Bank of America Stadium", "#0085CA", "#101820", "NFC", "NFC South"},
	{"New Orleans Saints", "Saints", "NO", "New Orleans", "Caesars Superdome", "#D3BC8D", "#101820", "NFC", "NFC South"},
	{"Tampa Bay Buccaneers", "Buccaneers", "TB", "Tampa", "Raymond James Stadium", "#D50A0A", "#34302B", "NFC", "NFC South"},
	// NFC West
	{"Arizona Cardinals", "Cardinals", "ARI", "Glendale", "State Farm Stadium", "#97233F", "#000000", "NFC", "NFC West"},
	{"Los Angeles Rams", "Rams", "LAR", "Inglewood", "SoFi Stadium", "#003594", "#FFA300", "NFC", "NFC West"},
	{"San Francisco 49ers", "49ers", "SF", "Santa Clara", "Levi's Stadium", "#AA0000", "#B3995D", "NFC", "NFC West"},
	{"Seattle Seahawks", "Seahawks", "SEA", "Seattle", "Lumen Field", "#002244", "#69BE28", "NFC", "NFC West"},
	// AFC East
	{"Buffalo Bills", "Bills", "BUF", "Orchard Park", "Highmark Stadium", "#00338D", "#C60C30", "AFC", "AFC East"},
	{"Miami Dolphins", "Dolphins", "MIA", "Miami Gardens", "Hard Rock Stadium", "#008E97", "#FC4C02", "AFC", "AFC East"},
	{"New England Patriots", "Patriots", "NE", "Foxborough", "Gillette Stadium", "#002244", "#C60C30", "AFC", "AFC East"},
	{"New York Jets", "Jets", "NYJ", "East Rutherford", "MetLife Stadium", "#125740", "#000000", "AFC", "AFC East"},
	// AFC North
	{"Baltimore Ravens", "Ravens", "BAL", "Baltimore", "M&T Bank Stadium", "#241773", "#000000", "AFC", "AFC North"},
	{"Cincinnati Bengals", "Bengals", "CIN", "Cincinnati", "Paycor Stadium", "#FB4F14", "#000000", "AFC", "AFC North"},
	{"Cleveland Browns", "Browns", "CLE", "Cleveland", "Huntington Bank Field", "#311D00", "#FF3C00", "AFC", "AFC North"},
	{"Pittsburgh Steelers", "Steelers", "PIT", "Pittsburgh", "Acrisure Stadium", "#FFB612", "#101820", "AFC", "AFC North"},
	// AFC South
	{"Houston Texans", "Texans", "HOU", "Houston", "NRG Stadium", "#03202F", "#A71930", "AFC", "AFC South"},
	{"Indianapolis Colts", "Colts", "IND", "Indianapolis", "Lucas Oil Stadium", "#002C5F", "#A2AAAD", "AFC", "AFC South"},
	{"Jacksonville Jaguars", "Jaguars", "JAX", "Jacksonville", "EverBank Stadium", "#006778", "#D7A22A", "AFC", "AFC South"},
	{"Tennessee Titans", "Titans", "TEN", "Nashville", "Nissan Stadium", "#0C2340", "#4B92DB", "AFC", "AFC South"},
	// AFC West
	{"Denver Broncos", "Broncos", "DEN", "Denver", "Empower Field at Mile High", "#FB4F14", "#002244", "AFC", "AFC West"},
	{"Kansas City Chiefs", "Chiefs", "KC", "Kansas City", "GEHA Field at Arrowhead Stadium", "#E31837", "#FFB81C", "AFC", "AFC West"},
	{"Las Vegas Raiders", "Raiders", "LV", "Las Vegas", "Allegiant Stadium", "#000000", "#A5ACAF", "AFC", "AFC West"},
	{"Los Angeles Chargers", "Chargers", "LAC", "Inglewood", "SoFi Stadium", "#0080C6", "#FFC20E", "AFC", "AFC West"},
}

// SeedNFLTeams inserts all 32 NFL teams if none exist for the NFL league.
// Safe to call repeatedly — it checks the team count first.
func SeedNFLTeams(ctx context.Context, db *sql.DB) error {
	// Look up NFL league ID
	var leagueID string
	err := db.QueryRowContext(ctx, `SELECT id FROM sports_leagues WHERE abbreviation = 'NFL'`).Scan(&leagueID)
	if err == sql.ErrNoRows {
		log.Printf("[sports/seed] NFL league not found, skipping NFL team seed")
		return nil
	}
	if err != nil {
		return err
	}

	// Check if teams already seeded
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sports_teams WHERE league_id = $1`, leagueID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		log.Printf("[sports/seed] NFL teams already seeded (%d teams), skipping", count)
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO sports_teams
		  (league_id, name, short_name, abbreviation, city, venue, primary_color, secondary_color, conference, division)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (league_id, abbreviation) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, t := range nflTeams {
		if _, err := stmt.ExecContext(ctx, leagueID,
			t.name, t.shortName, t.abbreviation, t.city, t.venue,
			t.primaryColor, t.secondaryColor, t.conference, t.division); err != nil {
			log.Printf("[sports/seed] insert team %s: %v", t.abbreviation, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("[sports/seed] seeded %d NFL teams", len(nflTeams))
	return nil
}
