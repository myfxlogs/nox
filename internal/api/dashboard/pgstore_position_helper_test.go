package dashboard

import (
	"time"

	"nox/internal/store/pgstore"
)

func pgstorePositionHelper() pgstore.Position {
	return pgstore.Position{OpportunityID: "D341:XAUUSD:a:b", Type: "XASSET_CARRY", Status: "OPEN"}
}

func timeMinuteHelper() time.Duration { return 30 * time.Minute }
