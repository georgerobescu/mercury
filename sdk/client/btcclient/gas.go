package btcclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/renproject/mercury/types"
	"github.com/renproject/mercury/types/btctypes"
	"github.com/sirupsen/logrus"
)

// GasStation retrieves the recommended tx fee from `bitcoinfees.earn.com`. It cached the result to avoid hitting the
// rate limiting of the API. It's safe for using concurrently.
type BtcGasStation interface {
	Initialized() bool
	GasRequired(ctx context.Context, speed types.TxSpeed, txSizeInBytes int) (btctypes.Amount, error)
}

type gasStation struct {
	mu            *sync.RWMutex
	logger        logrus.FieldLogger
	fees          map[types.TxSpeed]int64
	lastUpdate    time.Time
	minUpdateTime time.Duration
}

// NewGasStation returns a new GasStation
func NewGasStation(logger logrus.FieldLogger, minUpdateTime time.Duration) GasStation {
	return &gasStation{
		mu:            new(sync.RWMutex),
		logger:        logger,
		fees:          map[types.TxSpeed]int64{},
		lastUpdate:    time.Time{},
		minUpdateTime: minUpdateTime,
	}
}

func (btc gasStation) GasRequired(ctx context.Context, speed types.TxSpeed) int64 {
	btc.mu.Lock()
	defer btc.mu.Unlock()

	if time.Now().After(btc.lastUpdate.Add(btc.minUpdateTime)) {
		if err = btc.gasRequired(ctx); err != nil {
			btc.logger.Errorf("cannot get recommended fee from bitcoinfees.earn.com, err = %v", err)
		}
	}

	// Return an error if we were never able to fetch gas prices
	if !btc.Initialized() {
		return btctypes.Amount(0), fmt.Errorf("failed to fetch gas information: %v", err)
	}

func (btc gasStation) CalculateGasAmount(ctx context.Context, speed types.TxSpeed, txSizeInBytes int) btctypes.Amount {
	gasRequired := btc.GasRequired(ctx, speed) // in sats/byte
	gasInSats := gasRequired * int64(txSizeInBytes)
	return btctypes.Amount(gasInSats), nil
}

func (btc *gasStation) gasRequired(ctx context.Context) error {
	// FIXME: Use context for http request timeout
	response, err := http.Get("https://bitcoinfees.earn.com/api/v1/fees/recommended")
	if err != nil {
		return fmt.Errorf("cannot build request to bitcoinfees.earn.com = %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.WithContext(ctx)

	response, err := (&http.Client{}).Do(request)
	if err != nil {
		return fmt.Errorf("error submitting gas request: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code %v", response.StatusCode)
	}

	var fee = struct {
		Fast     int64 `json:"fastestFee"`
		Standard int64 `json:"halfHourFee"`
		Slow     int64 `json:"hourFee"`
	}{}
	if err := json.NewDecoder(response.Body).Decode(&fee); err != nil {
		return err
	}
	btc.fees[types.Fast] = fee.Fast
	btc.fees[types.Standard] = fee.Standard
	btc.fees[types.Slow] = fee.Slow
	btc.lastUpdate = time.Now()
	return nil
}
