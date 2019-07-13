package btcclient

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/iqoption/zecutil"
	"github.com/renproject/mercury/rpcclient/btcrpcclient"
	"github.com/renproject/mercury/types"
	"github.com/renproject/mercury/types/btctypes"
	"github.com/renproject/mercury/types/btctypes/btcaddress"
	"github.com/renproject/mercury/types/btctypes/btctx"
	"github.com/renproject/mercury/types/btctypes/btcutxo"
	"github.com/sirupsen/logrus"
)

const (
	Dust            = btctypes.Amount(600)
	ZecExpiryHeight = uint32(10000000)
	ZecVersion      = 4
	BtcVersion      = 2

	MainnetMercuryURL  = "206.189.83.88:5000/btc/mainnet"
	TestnetMercuryURL  = "206.189.83.88:5000/btc/testnet"
	LocalnetMercuryURL = "0.0.0.0:5000/btc/testnet"
)

var (
	ErrInvalidTxHash  = errors.New("invalid tx hash")
	ErrTxHashNotFound = errors.New("tx hash not found")
	ErrUTXOSpent      = errors.New("utxo spent or invalid index")
)

// Client is a client which is used to talking with certain Bitcoin network. It can interacting with the blockchain
// through Mercury server.
type client struct {
	client     btcrpcclient.Client
	chain      btctypes.Chain
	network    btctypes.Network
	config     chaincfg.Params
	url        string
	gasStation GasStation
	logger     logrus.FieldLogger
}

// New returns a new Client of given Bitcoin network.
func New(logger logrus.FieldLogger, network btctypes.Network) (Client, error) {
	config := &rpcclient.ConnConfig{
		HTTPPostMode: true,
		DisableTLS:   true,
	}

	switch network {
	case btctypes.Mainnet:
		config.Host = MainnetMercuryURL
	case btctypes.Testnet:
		config.Host = TestnetMercuryURL
	case btctypes.Localnet:
		config.Host = LocalnetMercuryURL
	default:
		panic("unknown bitcoin network")
	}

	gasStation := NewGasStation(logger, 30*time.Minute)
	return &client{
		network:    network,
		config:     *network.Params(),
		url:        config.Host,
		gasStation: gasStation,
		logger:     logger,
	}, nil
}

func (c *client) Network() btctypes.Network {
	return c.network
}

func (c *client) Chain() btctypes.Chain {
	return c.chain
}

// UTXO returns the UTXO for the given transaction hash and index.
func (c *client) UTXO(txHash types.TxHash, index uint32) (btcutxo.UTXO, error) {
	tx, err := c.client.GetRawTransactionVerbose([]string{string(txHash)})
	if err != nil {
		return nil, ErrTxHashNotFound
	}

	txOut, err := c.client.GetTxOut(string(txHash), index)
	if err != nil {
		return nil, fmt.Errorf("cannot get tx output from btc client: %v", err)
	}

	// TODO: add validation checking back
	// Check if UTXO has been spent.
	// if txOut == nil {
	// 	return nil, ErrUTXOSpent
	// }

	amount, err := btcutil.NewAmount(txOut.Value)
	if err != nil {
		return nil, fmt.Errorf("cannot parse amount received from btc client: %v", err)
	}
	return btcutxo.NewStandardUTXO(
		c.chain,
		types.TxHash(tx.TxID),
		btctypes.Amount(amount),
		txOut.ScriptPubKey.Hex,
		index,
		types.Confirmations(txOut.Confirmations),
	), nil
}

// UTXOsFromAddress returns the UTXOs for a given address. Important: this function will not return any UTXOs for
// addresses that have not been imported into the Bitcoin node.
func (c *client) UTXOsFromAddress(address btcaddress.Address) (btcutxo.UTXOs, error) {
	outputs, err := c.client.ListUnspent(0, 999999, []string{address.EncodeAddress()})
	if err != nil {
		return nil, fmt.Errorf("cannot retrieve utxos from btc client: %v", err)
	}

	utxos := make(btcutxo.UTXOs, len(outputs))
	for i, output := range outputs {
		amount, err := btcutil.NewAmount(output.Amount)
		if err != nil {
			return nil, fmt.Errorf("cannot parse amount received from btc client: %v", err)
		}

		utxos[i] = btcutxo.NewStandardUTXO(
			c.chain,
			types.TxHash(output.TxID),
			btctypes.Amount(amount),
			output.ScriptPubKey,
			output.Vout,
			types.Confirmations(output.Confirmations),
		)
	}

	return utxos, nil
}

// Confirmations returns the number of confirmation blocks of the given txHash.
func (c *client) Confirmations(txHash types.TxHash) (types.Confirmations, error) {
	tx, err := c.client.GetRawTransactionVerbose([]string{string(txHash)})
	if err != nil {
		return 0, fmt.Errorf("cannot get tx from hash %s: %v", txHash, err)
	}
	return types.Confirmations(tx.Confirmations), nil
}

func (c *client) BuildUnsignedTx(utxos btcutxo.UTXOs, recipients btcaddress.Recipients, refundTo btcaddress.Address, gas btctypes.Amount) (btctx.BtcTx, error) {
	// Pre-condition checks.
	if gas < Dust {
		return nil, fmt.Errorf("pre-condition violation: gas = %v is too low", gas)
	}

	amountFromUTXOs := utxos.Sum()
	if amountFromUTXOs < Dust {
		return nil, fmt.Errorf("pre-condition violation: amount=%v from utxos is less than dust=%v", amountFromUTXOs, Dust)
	}

	// Add an output for each recipient and sum the total amount that is being transferred to recipients.
	amountToRecipients := btctypes.Amount(0)
	for _, recipient := range recipients {
		amountToRecipients += recipient.Amount
	}

	// Check that we are not transferring more to recipients than available in the UTXOs (accounting for gas).
	amountToRefund := amountFromUTXOs - amountToRecipients - gas
	if amountToRefund < 0 {
		return nil, fmt.Errorf("insufficient balance: expected %v, got %v", amountToRecipients+gas, amountFromUTXOs)
	}

	// Add an output to refund the difference between the amount being transferred to recipients and the total amount
	// from the UTXOs.
	if amountToRefund > 0 {
		recipients = append(btcaddress.Recipients{
			{
				Address: refundTo,
				Amount:  amountToRefund,
			},
		}, recipients...)
	}

	// Get the signature hashes we need to sign.
	return c.createUnsignedTx(utxos, recipients)
}

// SubmitSignedTx submits the signed transaction and returns the transaction hash in hex.
func (c *client) SubmitSignedTx(stx btctx.BtcTx) (types.TxHash, error) {
	// Pre-condition checks
	if !stx.IsSigned() {
		return "", errors.New("pre-condition violation: cannot submit unsigned transaction")
	}
	if err := c.VerifyTx(stx); err != nil {
		return "", fmt.Errorf("pre-condition violation: transaction failed verification: %v", err)
	}

	data, err := stx.Serialize()
	if err != nil {
		return "", fmt.Errorf("pre-condition violation: serialization failed: %v", err)
	}

	txHash, err := c.client.SendRawTransaction(data)
	if err != nil {
		return "", fmt.Errorf("cannot send raw transaction using btc client: %v", err)
	}
	return types.TxHash(txHash), nil
}

func (c *client) EstimateTxSize(numUTXOs, numRecipients int) int {
	return 146*numUTXOs + 33*numRecipients + 10
}

func (c *client) VerifyTx(tx btctx.BtcTx) error {
	if c.chain != btctypes.Bitcoin {
		return nil
	}

	data, err := tx.Serialize()
	if err != nil {
		return fmt.Errorf("failed to serialize transaction")
	}
	msgTx := new(wire.MsgTx)
	msgTx.Deserialize(bytes.NewBuffer(data))

	for i, utxo := range tx.UTXOs() {
		scriptPubKey, err := hex.DecodeString(utxo.ScriptPubKey())
		if err != nil {
			return err
		}
		engine, err := txscript.NewEngine(scriptPubKey, msgTx, i,
			txscript.StandardVerifyFlags, txscript.NewSigCache(10),
			txscript.NewTxSigHashes(msgTx), int64(utxo.Amount()))
		if err != nil {
			return err
		}
		if err := engine.Execute(); err != nil {
			return err
		}
	}
	return nil
}

func (c *client) GasStation() GasStation {
	return c.gasStation
}

func (c *client) createUnsignedTx(utxos btcutxo.UTXOs, recipients btcaddress.Recipients) (btctx.BtcTx, error) {
	switch c.chain {
	case btctypes.ZCash:
		return zecCreateUnsignedTx(c.network, utxos, recipients)
	default:
		return nil, fmt.Errorf("unsupported blockchain: %d", c.chain)
	}
}

func zecCreateUnsignedTx(network btctypes.Network, utxos btcutxo.UTXOs, recipients btcaddress.Recipients) (btctx.BtcTx, error) {
	msgTx := zecutil.MsgTx{
		MsgTx:        wire.NewMsgTx(ZecVersion),
		ExpiryHeight: ZecExpiryHeight,
	}

	for _, utxo := range utxos {
		hash, err := chainhash.NewHashFromStr(utxo.ScriptPubKey())
		if err != nil {
			return nil, err
		}
		msgTx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(hash, utxo.Vout()), nil, nil))
	}

	for _, recipient := range recipients {
		script, err := zecutil.PayToAddrScript(recipient.Address)
		if err != nil {
			return nil, err
		}
		msgTx.AddTxOut(wire.NewTxOut(int64(recipient.Amount), script))
	}

	return btctx.NewUnsignedZecTx(network, utxos, &msgTx)
}

func btcCreateUnsignedTx(network btctypes.Network, utxos btcutxo.UTXOs, recipients btcaddress.Recipients) (btctx.BtcTx, error) {
	msgTx := wire.NewMsgTx(BtcVersion)
	for _, utxo := range utxos {
		hash, err := chainhash.NewHashFromStr(utxo.ScriptPubKey())
		if err != nil {
			return nil, err
		}
		msgTx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(hash, utxo.Vout()), nil, nil))
	}
	for _, recipient := range recipients {
		script, err := zecutil.PayToAddrScript(recipient.Address)
		if err != nil {
			return nil, err
		}
		msgTx.AddTxOut(wire.NewTxOut(int64(recipient.Amount), script))
	}
	return btctx.NewUnsignedBtcTx(network, utxos, msgTx)
}
