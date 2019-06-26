package btcaccount

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// Speed indicates the tier of speed that the transaction falls under while writing to the blockchain.
type Speed uint8

// TxExecutionSpeed values.
const (
	Nil = Speed(iota)
	Slow
	Standard
	Fast
)

// BitcoinFee retrieves the recommended tx fee from `bitcoinfees.earn.com`. It cached the result to avoid hitting the
// rate limiting of the API. It's safe for using concurrently.
type BitcoinFee struct {
	mu            *sync.RWMutex
	logger        logrus.FieldLogger
	fees          map[Speed]int64
	lastUpdate    time.Time
	minUpdateTime time.Duration
}

// NewBitcoinFee returns a
func NewBitcoinFee(logger logrus.FieldLogger, minUpdateTime time.Duration) *BitcoinFee {
	return &BitcoinFee{
		mu:            new(sync.RWMutex),
		logger:        logger,
		fees:          map[Speed]int64{},
		lastUpdate:    time.Time{},
		minUpdateTime: minUpdateTime,
	}
}

func (btc *BitcoinFee) RecommendedTxFee(speed Speed) int64 {
	btc.mu.Lock()
	defer btc.mu.Unlock()

	if time.Now().After(btc.lastUpdate.Add(btc.minUpdateTime)) {
		if err := btc.updateFee(); err != nil {
			btc.logger.Errorf("cannot get recommended fee from bitcoinfees.earn.com, err = %v", err)
		}
	}

	return btc.fees[speed]
}

func (btc *BitcoinFee) updateFee() error {
	response, err := http.Get("https://bitcoinfees.earn.com/api/v1/fees/recommended")
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unpected status code %v", response.StatusCode)
	}

	var fee = struct {
		Fast     int64 `json:"fastestFee"`
		Standard int64 `json:"halfHourFee"`
		Slow     int64 `json:"hourFee"`
	}{}
	if err := json.NewDecoder(response.Body).Decode(&fee); err != nil {
		return err
	}
	btc.fees[Fast] = fee.Fast
	btc.fees[Standard] = fee.Standard
	btc.fees[Slow] = fee.Slow
	btc.lastUpdate = time.Now()
	return nil
}
