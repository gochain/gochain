pragma solidity ^0.5.8;

contract Test {
    event TestEvent(string indexed value, address indexed addr, uint number);

    function Emit(string calldata value, address addr, uint number) external {
        emit TestEvent(value, addr, number);
    }
}