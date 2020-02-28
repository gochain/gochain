package cross

//go:generate wget -q https://raw.githubusercontent.com/gochain/gost/v0.0.4/contracts/Confirmations.sol -O Confirmations.sol
//go:generate wget -q https://raw.githubusercontent.com/gochain/gost/v0.0.4/contracts/Auth.sol -O Auth.sol
//go:generate wget -q https://raw.githubusercontent.com/gochain/gost/v0.0.4/contracts/AddressSet.sol -O AddressSet.sol
//go:generate abigen --sol Confirmations.sol --exc Confirmations.sol:IConfirmations,Auth.sol:Auth,Auth.sol:IAuth --pkg cross --out confirmations.go
//go:generate abigen --sol Test.sol --pkg cross_test --out cross_gen_test.go
