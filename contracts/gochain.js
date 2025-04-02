import { defineChain } from 'viem'

export const gochain = defineChain({
    id: 1331747801,
    name: 'GoChain',
    type: 'legacy',
    nativeCurrency: {
        decimals: 18,
        name: 'GO',
        symbol: 'GO',
    },
    rpcUrls: {
        default: {
            http: ['http://localhost:8545'],
            // webSocket: ['wss://rpc.zora.energy'],
        },
    },
    blockExplorers: {
        default: { name: 'Explorer', url: 'https://rpc.gochain.io' },
    },
})