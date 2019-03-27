package main

import (
	"encoding/binary"
	"strings"
	"os"
	"fmt"
	"time"
	"context"
	"strconv"
	"bytes"
	crand "crypto/rand"
	rand "math/rand"

	pb "github.com/decred/dcrwallet/rpc/walletrpc"
	"github.com/decred/dcrd/dcrutil"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/txscript"

	flags "github.com/jessevdk/go-flags"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"golang.org/x/crypto/ssh/terminal"
)

// feeRate in Atoms/KB.
const feeRate = int64(1e4)

type opts struct {
	WalletGRPCServer string `short:"w" long:"walletrpcserver" description:"Address and port for the wallet's GRPC server"`
	RPCCert string `short:"c" long:"rpccert" description:"Location of the wallet's rpc.cert file"`
	Utxos []string `short:"u" long:"utxo" description:"Utxos to consume (in the format txid:output_index)"`
	DestAddrs []string `long:"dest" description:"Destination addresses to send to"`
	DestAmounts []float64 `long:"amt" description:"Amount to send to given destination"`
	ChangeAddr string `long:"changeto" description:"Address to send all change to"`
	ChangeAccount int `long:"changeaccount" description:"Account to send all change to"`
	Sign bool `long:"sign" description:"If specified, sign the transaction. Password needs to be provided on stdin."`
	Publish bool `long:"publish" description:"If specified, publish the generated transaction (requires --sign)"`
}

func getCmdOpts() *opts {
	cmdOpts := &opts{}
	parser := flags.NewParser(cmdOpts, flags.Default)
	_, err := parser.Parse()
	if err != nil {
		e, ok := err.(*flags.Error)
		if ok && e.Type == flags.ErrHelp {
			os.Exit(0)
		}
		orPanic(err)
	}

	return cmdOpts
}

type cryptoSource struct{}

func (s cryptoSource) Seed(seed int64) {}

func (s cryptoSource) Int63() int64 {
    return int64(s.Uint64() & ^uint64(1<<63))
}

func (s cryptoSource) Uint64() (v uint64) {
    err := binary.Read(crand.Reader, binary.BigEndian, &v)
    orPanic(err)
    return v
}

func orPanic(err error) {
	if err != nil {
		panic(err)
	}
}

func readPass() []byte {
	fmt.Print("Type wallet password: ")
	var pass []byte
	var err error
	pass, err = terminal.ReadPassword(int(os.Stdin.Fd()))
	orPanic(err)
	fmt.Print("\n")

	pass = bytes.TrimSpace(pass)
	if len(pass) == 0 {
		return nil
	}

	return pass
}

func main() {
	opts := getCmdOpts()
	if len(opts.DestAddrs) == 0 {
		fmt.Println("No destination addresses specified")
		os.Exit(1)
	}

	if len(opts.DestAddrs) != len(opts.DestAmounts) {
		fmt.Println("Number of destination addresses and amounts different")
		os.Exit(1)
	}

	if len(opts.Utxos) == 0 {
		fmt.Println("No utxos specified")
		os.Exit(1)
	}

	if opts.Publish && !opts.Sign {
		fmt.Println("--publish requires --sign")
		os.Exit(1)
	}

	creds, err := credentials.NewClientTLSFromFile(opts.RPCCert, "localhost")
	orPanic(err)

	ctxb := context.Background()
	ctx, cancel := context.WithTimeout(ctxb, time.Second*5)
	defer cancel()

	conn, err := grpc.DialContext(ctx, opts.WalletGRPCServer, grpc.WithTransportCredentials(creds))
	orPanic(err)
	defer conn.Close()
	c := pb.NewWalletServiceClient(conn)

	var changeAddr dcrutil.Address
	if opts.ChangeAddr != "" {
		// If the change address was specified, verify if it was from the wallet.
		changeAddr, err = dcrutil.DecodeAddress(opts.ChangeAddr)
		orPanic(err)

		validateAddrResp, err := c.ValidateAddress(ctxb, &pb.ValidateAddressRequest{
			Address: opts.ChangeAddr,
		})
		orPanic(err)

		if !validateAddrResp.IsMine {
			fmt.Println("Change address is not mine.")
			os.Exit(1)
		}
	} else {
		// Else, generate a new address from the specified account (defaults to
		// account 0).
		nextAddrResp, err := c.NextAddress(ctxb, &pb.NextAddressRequest{
			Account: uint32(opts.ChangeAccount),
			Kind: pb.NextAddressRequest_BIP0044_INTERNAL,
		})
		orPanic(err)

		changeAddr, err = dcrutil.DecodeAddress(nextAddrResp.Address)
		orPanic(err)
	}


	// Verify all utxos are from the wallet.
	tx := wire.NewMsgTx()
	var totalInputAmount int64
	for i := 0; i < len(opts.Utxos); i++ {
		txh := new(chainhash.Hash)
		split := strings.Split(opts.Utxos[i], ":")
		if len(split) != 2 {
			fmt.Printf("invalid utxo at index %d\n", i)
			os.Exit(1)
		}

		err := chainhash.Decode(txh, split[0])
		orPanic(err)

		respGetTx, err := c.GetTransaction(ctxb, &pb.GetTransactionRequest{
			TransactionHash: txh[:],
		})
		orPanic(err)

		if respGetTx.Transaction == nil {
			fmt.Println("transaction not found: %s\n", txh)
			os.Exit(1)
		}

		tree := int8(0)
		switch respGetTx.Transaction.TransactionType {
		case pb.TransactionDetails_TICKET_PURCHASE:
			fallthrough
		case pb.TransactionDetails_VOTE:
			fallthrough
		case pb.TransactionDetails_REVOCATION:
			tree = 1
		}

		foundCredit := false
		targetIndex, err := strconv.Atoi(split[1])
		orPanic(err)
		for _, credit := range respGetTx.Transaction.Credits {
			if int(credit.Index) != targetIndex {
				continue
			}
			foundCredit = true
			tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(txh, credit.Index, tree),
				credit.Amount, nil))
			totalInputAmount += credit.Amount

			lenOutScript := len(credit.OutputScript)
			// TODO: this is brittle. Improve.
			if (tree == 0 && lenOutScript != 25) || (tree == 1 && lenOutScript != 26) {
				fmt.Println("length of output script implies it's not a p2pkh, so not supported at this time.")
				os.Exit(1)
			}
			break
		}

		if !foundCredit {
			fmt.Printf("could not find utxo %s in wallet\n", opts.Utxos[i])
			os.Exit(1)
		}
	}

	var totalOutputAmount int64
	for i := 0; i < len(opts.DestAddrs); i++ {
		addr, err := dcrutil.DecodeAddress(opts.DestAddrs[i])
		orPanic(err)

		amt, err := dcrutil.NewAmount(opts.DestAmounts[i])
		orPanic(err)

		pkscript, err := txscript.PayToAddrScript(addr)
		orPanic(err)

		tx.AddTxOut(wire.NewTxOut(int64(amt), pkscript))
		totalOutputAmount += int64(amt)
	}

	// How many bytes it will take to send a change.
	changeOutputSize := int64(8 + 2 + 1 + 25)

	totalBytes := int64(tx.SerializeSize())
	totalBytes += int64(len(tx.TxIn) * (1 + 73 + 1 + 33)) // Add size of signature

	feeWithChange := (totalBytes + changeOutputSize) * feeRate / 1000
	feeWithoutChange := totalBytes * feeRate / 1000

	// Pretty rough dust limit calc for now.
	dustLimit := 200 * feeRate / 1000

	if totalInputAmount - totalOutputAmount - feeWithoutChange < dustLimit {
		fmt.Printf("cannot send %d from %d without change due to dust limit of %d\n",
			totalOutputAmount, totalInputAmount, dustLimit)
		os.Exit(1)
	}

	var changeAmt int64
	estimatedBytes := totalBytes
	estimatedFee := feeWithoutChange
	if totalInputAmount - totalOutputAmount - feeWithChange > dustLimit {
		// Will have change after sending. Create change output.
		pkscript, err := txscript.PayToAddrScript(changeAddr)
		orPanic(err)

		changeAmt = totalInputAmount - totalOutputAmount - feeWithChange
		estimatedBytes = totalBytes + changeOutputSize
		estimatedFee = feeWithChange

		tx.AddTxOut(wire.NewTxOut(changeAmt, pkscript))
	}

	// Shuffle outputs.
	rnd := rand.New(cryptoSource{})
	rnd.Shuffle(len(tx.TxIn), func (i, j int) {
		tx.TxIn[i], tx.TxIn[j] = tx.TxIn[j], tx.TxIn[i]
	})


	unsignedBts, err := tx.Bytes()
	orPanic(err)

	if opts.Sign {
		passphrase := readPass()
		defer func () {
			copy(passphrase, bytes.Repeat([]byte{0}, len(passphrase)))
		}()

		signResp, err := c.SignTransaction(ctxb, &pb.SignTransactionRequest{
			SerializedTransaction: unsignedBts,
			Passphrase: passphrase,
		})
		orPanic(err)

		if opts.Publish {
			pubResp, err := c.PublishTransaction(ctxb, &pb.PublishTransactionRequest{
				SignedTransaction: signResp.Transaction,
			})
			orPanic(err)
			txhPub, err := chainhash.NewHash(pubResp.TransactionHash)
			orPanic(err)
			fmt.Printf("Published tx %s\n", txhPub.String())
		} else {
			fmt.Println("Serialized **SIGNED** Tx:")
			fmt.Printf("%x\n\n", signResp.Transaction)
		}
	} else {
		fmt.Println("Serialized unsigned tx:")
		fmt.Printf("%x\n\n", unsignedBts)
	}

	fmt.Printf("Estimated fee: %s for %d bytes\n", dcrutil.Amount(estimatedFee),
		estimatedBytes)
	if changeAmt == 0 {
		fmt.Println("Zero change.")
	} else {
		fmt.Printf("Sent %s as change to %s\n", dcrutil.Amount(changeAmt),
			changeAddr.EncodeAddress())
	}
}