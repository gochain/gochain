import { createPublicClient, createWalletClient, http } from 'viem'
import { privateKeyToAccount } from 'viem/accounts'
import { gochain } from './gochain.js'
import GasPrice from './gasPrice.json' with { type: 'json' }

const account = privateKeyToAccount(process.env.PK)

const publicClient = createPublicClient({
    chain: gochain,
    transport: http(),
})

const wallet = createWalletClient({
    account: account,
    chain: gochain,
    transport: http()
})

const hash = await wallet.deployContract({
    abi: GasPrice.abi,
    account: account,
    bytecode: GasPrice.bytecode,
})

console.log(hash)