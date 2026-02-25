// models.go — Data types for the sports service.
package sports

import "time"

// League represents a sports league.
type League struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Abbreviation     string    `json:"abbreviation"`
	Sport            string    `json:"sport"`
	CountryCode      *string   `json:"country_code,omitempty"`
	LogoURL          *string   `json:"logo_url,omitempty"`
	SeasonStructure  string    `json:"season_structure"`
	TheSportsDBID    *string   `json:"thesportsdb_id,omitempty"`
	APIFootballID    *string   `json:"api_football_id,omitempty"`
	IsActive         bool      `json:"is_active"`
	SortOrder        int       `json:"sort_order"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Team represents a sports team.
type Team struct {
	ID             string    `json:"id"`
	LeagueID       string    `json:"league_id"`
	Name           string    `json:"name"`
	ShortName      *string   `json:"short_name,omitempty"`
	Abbreviation   string    `json:"abbreviation"`
	City           *string   `json:"city,omitempty"`
	Venue          *string   `json:"venue,omitempty"`
	LogoURL        *string   `json:"logo_url,omitempty"`
	PrimaryColor   *string   `json:"primary_color,omitempty"`
	SecondaryColor *string   `json:"secondary_color,omitempty"`
	Conference     *string   `json:"conference,omitempty"`
	Division       *string   `json:"division,omitempty"`
	TheSportsDBID  *string   `json:"thesportsdb_id,omitempty"`
	IsActive       bool      `json:"is_active"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Event represents a sports event/game.
type Event struct {
	ID                 string          `json:"id"`
	LeagueID           string          `json:"league_id"`
	HomeTeamID         *string         `json:"home_team_id,omitempty"`
	AwayTeamID         *string         `json:"away_team_id,omitempty"`
	Season             string          `json:"season"`
	SeasonType         string          `json:"season_type"`
	Week               *string         `json:"week,omitempty"`
	Venue              *string         `json:"venue,omitempty"`
	ScheduledTime      time.Time       `json:"scheduled_time"`
	ActualStartTime    *time.Time      `json:"actual_start_time,omitempty"`
	Status             string          `json:"status"`
	HomeScore          int             `json:"home_score"`
	AwayScore          int             `json:"away_score"`
	Period             *string         `json:"period,omitempty"`
	PeriodScores       string          `json:"period_scores"`
	BroadcastInfo      string          `json:"broadcast_info"`
	TheSportsDBEventID *string         `json:"thesportsdb_event_id,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	ChannelMappings    []ChannelMapping `json:"channel_mappings,omitempty"`
}

// ChannelMapping links a sports event to a channel.
type ChannelMapping struct {
	ID        string    `json:"id"`
	EventID   string    `json:"event_id"`
	ChannelID *string   `json:"channel_id,omitempty"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
	IsPrimary bool      `json:"is_primary"`
	Notes     *string   `json:"notes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// TheSportsDBEvent represents the JSON structure from TheSportsDB API.
type TheSportsDBEvent struct {
	IDEvent        string  `json:"idEvent"`
	StrEvent       string  `json:"strEvent"`
	IDHomeTeam     string  `json:"idHomeTeam"`
	IDAwayTeam     string  `json:"idAwayTeam"`
	StrHomeTeam    string  `json:"strHomeTeam"`
	StrAwayTeam    string  `json:"strAwayTeam"`
	IntHomeScore   *string `json:"intHomeScore"`
	IntAwayScore   *string `json:"intAwayScore"`
	StrStatus      string  `json:"strStatus"`
	DateEvent      string  `json:"dateEvent"`
	StrTime        string  `json:"strTime"`
	StrSeason      string  `json:"strSeason"`
	IntRound       *string `json:"intRound"`
	StrVenue       string  `json:"strVenue"`
}

// TheSportsDBEventsResponse is the top-level response from the events API.
type TheSportsDBEventsResponse struct {
	Events []TheSportsDBEvent `json:"events"`
}

// TheSportsDBTeam represents a team from TheSportsDB.
type TheSportsDBTeam struct {
	IDTeam          string `json:"idTeam"`
	StrTeam         string `json:"strTeam"`
	StrTeamShort    string `json:"strTeamShort"`
	StrAlternate    string `json:"strAlternate"`
	StrStadium      string `json:"strStadium"`
	StrTeamBadge    string `json:"strTeamBadge"`
	StrColour1      string `json:"strColour1"`
	StrColour2      string `json:"strColour2"`
	StrDescriptionEN string `json:"strDescriptionEN"`
}

// TheSportsDBTeamsResponse is the top-level teams response.
type TheSportsDBTeamsResponse struct {
	Teams []TheSportsDBTeam `json:"teams"`
}
