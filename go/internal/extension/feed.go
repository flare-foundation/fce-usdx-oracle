package extension

import (
	"context"
	"strconv"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/wire/feedupdate"

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
		return refuse(failure.Coerce(reqErr, failure.Internal))
	}
	// Refused here rather than observed and signed. An update naming another feed
	// would be rejected by that feed's store anyway, but only after this machine
	// had spent an observation on it and published the answer to a public log.
	if feedID != config.FeedID {
		return refuse(failure.UnexpectedPayload)
	}

	// df.Timestamp is the block timestamp of the instruction event, cosigned by
	// the data-provider quorum along with the rest of DataFixed. Always a real
	// block here — GET_FEED is instruction-only, so this handler has one. The
	// observer treats it as the clock this machine is checked against; it never
	// becomes the update's observedAt, which must stay the moment the inputs
	// were read.
	obs, err := e.observer.Report(context.Background(), df.Timestamp)

	// Health is reported either way: on failure it is what tells an operator
	// WHICH source broke.
	e.mu.Lock()
	e.sourceHealth = obs.Health
	e.mu.Unlock()

	if err != nil {
		return refuse(failure.Coerce(err, failure.Internal))
	}

	e.mu.Lock()
	e.lastPrice = strconv.FormatInt(int64(obs.Value), 10)
	e.lastDecimals = obs.Decimals
	e.lastObservedAt = obs.ObservedAt
	e.updateCount++
	e.lastErrorClass = failure.ClassNone
	e.mu.Unlock()

	return succeed(obs.Data)
}
