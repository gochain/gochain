import { defineChain } from 'viem'

export const gochain = defineChain({
    id: 60, // replace with your local chain id when testing
    name: 'GoChain',
    type: 'legacy',
    nativeCurrency: {
        decimals: 18,
        name: 'GO',
        symbol: 'GO',
    },
    rpcUrls: {
        default: {
            http: ['https://rpc.gochain.io'], // need to set to https for prod 
        },
    },
    blockExplorers: {
        default: { name: 'Explorer', url: 'https://rpc.gochain.io' },
    },
})