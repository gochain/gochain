// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

import "@openzeppelin/contracts/access/Ownable.sol";

contract GasPrice is Ownable {
    uint256 public gasPrice;

    constructor() Ownable(msg.sender){
        gasPrice = 100000 gwei; // per GIP-38
    }

    /**
     * @dev Set the gas price.
     *
     * @param _gasPrice The new gas price.
     * This function is only callable by the contract owner.
     */
    function setGasPrice(uint256 _gasPrice) public onlyOwner {
        gasPrice = _gasPrice;
    }
}
