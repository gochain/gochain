import { createPublicClient, createWalletClient, getContractAddress, http } from 'viem'
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
    account: account,
    abi: GasPrice.abi,
    bytecode: '0x' + GasPrice.bytecode,
    args: [],
    gas: 1000000,
})
const receipt = await publicClient.waitForTransactionReceipt({ hash })
console.log(receipt)
