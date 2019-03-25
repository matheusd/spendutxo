# Utxo Spender Tool

This is a simple, quick & dirty tool to spend a specific utxo from your decred wallet.

You can specify which utxo(s) you want to spend, destination addresses and a change address or account. A transaction will be constructed and (optionally) signed and broadcast.

This is useful for situations where you want to ensure a specific set of utxos is selected for spending (e.g. spanning multiple accounts or because you want to control which utxos gets selected) and you want to spend it in a specific way (e.g. with the change going to yet another account).

## Building

Use go 1.12, with `GO111MODULES=on` or outside your `$GOPATH`. Just `go {run,build,install} .`.

## Usage

```
# Help!
$ spendutxo -h

# Generate the spend tx. You can specify multiple (--dest,--amt) pairs and
# also multiple -u input utxos.
$ spendutxo \
  -w localhost:19121 -c ~/.dcrwallet/rpc.cert \
  --dest TsfDLrRkk9ciUuwfp2b8PawwnukYD7yAjGd --amt 1.32 \
  -u 43b9e0cc1bfb1ca220e3a9d2be8c637aa4b9581eb0eab379dab7a674ce187cb3:1 \
  --changeaccount 3 

# Generate, Sign & Publish ([...] is the rest of the arguments)
$ spendutxo [...] --sign --publish
```
