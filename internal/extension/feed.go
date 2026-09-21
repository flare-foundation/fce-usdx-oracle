package extension

import (
	"context"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/pkg/wire/feedupdate"

	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
)

// processGetFeed signs the latest observation and returns the payload. Fails
// closed: never guesses, never falls back to a cached price. A frozen feed with
// an ageing timestamp is the designed failure mode.
func (e *Extension) processGetFeed(df *instruction.DataFixed) outcome {
	// The request names a feed, because one sender serves every feed of its
	// extension. Anything that is not exactly one ABI-encoded FeedUpdateRequest
	// is refused rather than guessed at, and nothing about it is echoed back:
	// ActionResult.Log is public.
	feedID, reqErr := feedupdate.DecodeFeedRequest(df.OriginalMessage)
	if reqErr != nil {
		return refuse(failure.UnexpectedPayload)
	}
	// Refused here rather than observed and signed. An update naming another feed
	// would be rejected by that feed's store anyway, but only after this machine
	// had spent an observation on it and published the answer to a public log.
	if feedID != config.FeedID {
		return refuse(failure.UnexpectedPayload)
	}

	// df.Timestamp is the block timestamp of the instruction event, cosigned by
	// the data-provider quorum along with the rest of DataFixed. Always a real
	// block here — GET_FEED is instruction-only, so this handler has one. It is
	// the observedAt the update is signed with, as the store defines the field,
	// and the clock the reading's age is checked against. Which source broke on
	// a failure, and how the reading is ageing, are the readiness report's to
	// tell; nothing is recorded here.
	obs, err := e.observer.Report(context.Background(), df.Timestamp)
	if err != nil {
		return refuse(failure.Coerce(err, failure.Internal))
	}
	return succeed(obs.Data)
}
