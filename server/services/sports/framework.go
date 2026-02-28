// framework.go — Multi-sport framework.
// P15-T07: Sport-specific configuration used by the sports service and
// referenced via the sports_events.season_structure column.
package sports

// SportConfig holds sport-specific configuration: period names, scoring rules,
// commercial break detection hints, and other sport-level metadata.
type SportConfig struct {
	// Sport is the canonical sport identifier (e.g. "american_football").
	Sport string `json:"sport"`

	// DisplayName is the human-readable sport name.
	DisplayName string `json:"display_name"`

	// PeriodNames maps internal period codes to human-readable labels.
	PeriodNames map[string]string `json:"period_names"`

	// RegularPeriods is the ordered list of regular-time period codes.
	RegularPeriods []string `json:"regular_periods"`

	// OvertimePeriod is the period code used for overtime (may be empty).
	OvertimePeriod string `json:"overtime_period,omitempty"`

	// ShootoutPeriod is the period code used for shootout (may be empty).
	ShootoutPeriod string `json:"shootout_period,omitempty"`

	// ScoreLabel is the label used for a single scoring unit (e.g. "points", "runs", "goals").
	ScoreLabel string `json:"score_label"`

	// HasInnings indicates whether the sport uses innings rather than periods.
	HasInnings bool `json:"has_innings"`

	// TypicalDurationMinutes is the approximate game length, used for DVR buffer sizing.
	TypicalDurationMinutes int `json:"typical_duration_minutes"`

	// CommercialBreakMinDuration is the minimum break length (seconds) to consider a
	// commercial break for commercial-skip detection.
	CommercialBreakMinDuration int `json:"commercial_break_min_duration_seconds"`

	// HalftimeMinDuration is the minimum halftime/intermission length (seconds).
	HalftimeMinDuration int `json:"halftime_min_duration_seconds"`
}

// predefinedSportConfigs holds sport configs for all supported sports.
var predefinedSportConfigs = map[string]SportConfig{
	"american_football": {
		Sport:                      "american_football",
		DisplayName:                "American Football",
		PeriodNames:                map[string]string{"Q1": "1st Quarter", "Q2": "2nd Quarter", "Q3": "3rd Quarter", "Q4": "4th Quarter", "OT": "Overtime", "OT2": "2nd Overtime"},
		RegularPeriods:             []string{"Q1", "Q2", "Q3", "Q4"},
		OvertimePeriod:             "OT",
		ScoreLabel:                 "points",
		HasInnings:                 false,
		TypicalDurationMinutes:     210,
		CommercialBreakMinDuration: 90,
		HalftimeMinDuration:        900,
	},
	"basketball": {
		Sport:                      "basketball",
		DisplayName:                "Basketball",
		PeriodNames:                map[string]string{"Q1": "1st Quarter", "Q2": "2nd Quarter", "Q3": "3rd Quarter", "Q4": "4th Quarter", "OT": "Overtime", "OT2": "2nd Overtime", "OT3": "3rd Overtime"},
		RegularPeriods:             []string{"Q1", "Q2", "Q3", "Q4"},
		OvertimePeriod:             "OT",
		ScoreLabel:                 "points",
		HasInnings:                 false,
		TypicalDurationMinutes:     150,
		CommercialBreakMinDuration: 60,
		HalftimeMinDuration:        900,
	},
	"baseball": {
		Sport:                      "baseball",
		DisplayName:                "Baseball",
		PeriodNames:                map[string]string{"1": "1st", "2": "2nd", "3": "3rd", "4": "4th", "5": "5th", "6": "6th", "7": "7th", "8": "8th", "9": "9th", "E": "Extra"},
		RegularPeriods:             []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"},
		ScoreLabel:                 "runs",
		HasInnings:                 true,
		TypicalDurationMinutes:     180,
		CommercialBreakMinDuration: 120,
		HalftimeMinDuration:        0,
	},
	"ice_hockey": {
		Sport:                      "ice_hockey",
		DisplayName:                "Ice Hockey",
		PeriodNames:                map[string]string{"P1": "1st Period", "P2": "2nd Period", "P3": "3rd Period", "OT": "Overtime", "SO": "Shootout"},
		RegularPeriods:             []string{"P1", "P2", "P3"},
		OvertimePeriod:             "OT",
		ShootoutPeriod:             "SO",
		ScoreLabel:                 "goals",
		HasInnings:                 false,
		TypicalDurationMinutes:     150,
		CommercialBreakMinDuration: 75,
		HalftimeMinDuration:        1080,
	},
	"soccer": {
		Sport:                      "soccer",
		DisplayName:                "Soccer",
		PeriodNames:                map[string]string{"1H": "1st Half", "2H": "2nd Half", "ET1": "Extra Time 1st Half", "ET2": "Extra Time 2nd Half", "PEN": "Penalty Shootout"},
		RegularPeriods:             []string{"1H", "2H"},
		OvertimePeriod:             "ET1",
		ShootoutPeriod:             "PEN",
		ScoreLabel:                 "goals",
		HasInnings:                 false,
		TypicalDurationMinutes:     120,
		CommercialBreakMinDuration: 0,
		HalftimeMinDuration:        900,
	},
	"mls": {
		Sport:                      "soccer",
		DisplayName:                "Major League Soccer",
		PeriodNames:                map[string]string{"1H": "1st Half", "2H": "2nd Half", "ET1": "Extra Time 1st Half", "ET2": "Extra Time 2nd Half", "PEN": "Penalty Shootout"},
		RegularPeriods:             []string{"1H", "2H"},
		OvertimePeriod:             "ET1",
		ShootoutPeriod:             "PEN",
		ScoreLabel:                 "goals",
		HasInnings:                 false,
		TypicalDurationMinutes:     120,
		CommercialBreakMinDuration: 0,
		HalftimeMinDuration:        900,
	},
	"premier_league": {
		Sport:                      "soccer",
		DisplayName:                "Premier League",
		PeriodNames:                map[string]string{"1H": "1st Half", "2H": "2nd Half", "ET1": "Extra Time 1st Half", "ET2": "Extra Time 2nd Half", "PEN": "Penalty Shootout"},
		RegularPeriods:             []string{"1H", "2H"},
		OvertimePeriod:             "ET1",
		ShootoutPeriod:             "PEN",
		ScoreLabel:                 "goals",
		HasInnings:                 false,
		TypicalDurationMinutes:     120,
		CommercialBreakMinDuration: 0,
		HalftimeMinDuration:        900,
	},
}

// GetSportConfig returns the SportConfig for a given sport identifier.
// Falls back to a generic config if the sport is not recognized.
func GetSportConfig(sport string) SportConfig {
	if cfg, ok := predefinedSportConfigs[sport]; ok {
		return cfg
	}
	// Generic fallback
	return SportConfig{
		Sport:                      sport,
		DisplayName:                sport,
		PeriodNames:                map[string]string{"P1": "Period 1", "P2": "Period 2"},
		RegularPeriods:             []string{"P1", "P2"},
		ScoreLabel:                 "points",
		HasInnings:                 false,
		TypicalDurationMinutes:     120,
		CommercialBreakMinDuration: 60,
		HalftimeMinDuration:        600,
	}
}

// SupportedSports returns all sport identifiers with predefined configs.
func SupportedSports() []string {
	sports := make([]string, 0, len(predefinedSportConfigs))
	for k := range predefinedSportConfigs {
		sports = append(sports, k)
	}
	return sports
}
