delegator
=========

The delegator is a combination of contracts that allows a new contract to be
deployed and have its underlying code changed (but maintain the storage).


## Development

If you update the `delegator.sol` code then you'll need to recompile and copy
the hex binary code into the `delegator.go` constants.

```sh
$ solcjs --abi --bin delegator.sol
```

