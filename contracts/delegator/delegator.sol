pragma solidity ^0.4.24;

contract DelegateStorage {
  function invoke(address target, bytes msgdata) public {
    // solium-disable-next-line security/no-inline-assembly
    assembly {
      let result := delegatecall(sub(gas, 10000), target, add(msgdata, 0x20), mload(msgdata), 0, 0)
      let size := returndatasize
      let ptr := mload(0x40)
      returndatacopy(ptr, 0, size)

      switch result
      case 0 { revert(ptr, size) }
      default { return(ptr, size) }
    }
  }
}

contract Delegator {
  address private target = 0xEEfFEEffeEffeeFFeeffeeffeEfFeEffeEFfEeff;
  DelegateStorage private ds = DelegateStorage(0xaABBaaBBAaBbaAbBaabBAaBBaAbBAabBAaBBaAbB);

  function getTarget() public view returns (address) {
    return target;
  }

  function setTarget(address _target) public {
    target = _target;
  }

  function getDelegateStorage() public view returns (DelegateStorage) {
    return ds;
  }

  function() public {
    bytes memory msgdata = msg.data;
    address t = target;

    // solium-disable-next-line security/no-inline-assembly
    assembly {
      let result := call(sub(gas, 10000), t, 0, add(msgdata, 0x20), mload(msgdata), 0, 0)
      let size := returndatasize
      let ptr := mload(0x40)
      returndatacopy(ptr, 0, size)

      switch result
      case 0 { revert(ptr, size) }
      default { return(ptr, size) }
    }
  }
}
