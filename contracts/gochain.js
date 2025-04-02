import { defineChain } from 'viem'

export const gochain = defineChain({
    id: 60,
    name: 'GoChain',
    nativeCurrency: {
        decimals: 18,
        name: 'GO',
        symbol: 'GO',
    },
    rpcUrls: {
        default: {
            http: ['https://localhost:8545'],
            // webSocket: ['wss://rpc.zora.energy'],
        },
    },
    blockExplorers: {
        default: { name: 'Explorer', url: 'https://rpc.gochain.io' },
    },
})