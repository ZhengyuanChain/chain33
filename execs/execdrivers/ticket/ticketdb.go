package ticket

//database opeartion for execs ticket
import (
	"fmt"

	"code.aliyun.com/chain33/chain33/account"
	"code.aliyun.com/chain33/chain33/common"
	dbm "code.aliyun.com/chain33/chain33/common/db"
	"code.aliyun.com/chain33/chain33/types"
	log "github.com/inconshreveable/log15"
)

var tlog = log.New("module", "ticket.db")
var genesisKey = []byte("mavl-acc-genesis")
var addrSeed = []byte("address seed bytes for public key")

type TicketDB struct {
	types.Ticket
	prevstatus int32
}

func NewTicketDB(id, minerAddress, returnWallet string, blocktime int64, isGenesis bool) *TicketDB {
	t := &TicketDB{}
	t.TicketId = id
	t.MinerAddress = minerAddress
	t.ReturnAddress = returnWallet
	t.CreateTime = blocktime
	t.Status = 1
	t.IsGenesis = isGenesis
	t.prevstatus = 0
	return t
}

//ticket 的状态变化：
//1. status == 1 (NewTicket的情况)
//2. status == 2 (已经挖矿的情况)
//3. status == 3 (Close的情况)

//add prevStatus:  便于回退状态，以及删除原来状态
//list 保存的方法:
//minerAddress:status:ticketId=ticketId
func (t *TicketDB) GetReceiptLog() *types.ReceiptLog {
	log := &types.ReceiptLog{}
	if t.Status == 1 {
		log.Ty = types.TyLogNewTicket
	} else if t.Status == 2 {
		log.Ty = types.TyLogMinerTicket
	} else if t.Status == 3 {
		log.Ty = types.TyLogCloseTicket
	}
	r := &types.ReceiptTicket{}
	r.TicketId = t.TicketId
	r.Status = t.Status
	r.PrevStatus = t.prevstatus
	r.Addr = t.MinerAddress
	log.Log = types.Encode(r)
	return log
}

func (t *TicketDB) GetKVSet() (kvset []*types.KeyValue) {
	value := types.Encode(&t.Ticket)
	kvset = append(kvset, &types.KeyValue{TicketKey(t.TicketId), value})
	return kvset
}

func (t *TicketDB) Save(db dbm.KVDB) {
	set := t.GetKVSet()
	for i := 0; i < len(set); i++ {
		db.Set(set[i].GetKey(), set[i].Value)
	}
}

//address to save key
func TicketKey(id string) (key []byte) {
	key = append(key, []byte("mavl-ticket-")...)
	key = append(key, []byte(id)...)
	return key
}

func TicketBindKey(id string) (key []byte) {
	key = append(key, []byte("mavl-ticket-tbind-")...)
	key = append(key, []byte(id)...)
	return key
}

type TicketAction struct {
	db        dbm.KVDB
	txhash    []byte
	fromaddr  string
	blocktime int64
	height    int64
	execaddr  string
}

func NewTicketAction(db dbm.KVDB, tx *types.Transaction, execaddr string, blocktime, height int64) *TicketAction {
	hash := tx.Hash()
	fromaddr := account.PubKeyToAddress(tx.GetSignature().GetPubkey()).String()
	return &TicketAction{db, hash, fromaddr, blocktime, height, execaddr}
}

func (action *TicketAction) GenesisInit(genesis *types.TicketGenesis) (*types.Receipt, error) {
	prefix := common.ToHex(action.txhash)
	prefix = genesis.MinerAddress + ":" + prefix + ":"
	var logs []*types.ReceiptLog
	var kv []*types.KeyValue
	for i := 0; i < int(genesis.Count); i++ {
		id := prefix + fmt.Sprintf("%010d", i)
		t := NewTicketDB(id, genesis.MinerAddress, genesis.ReturnAddress, action.blocktime, true)
		//冻结子账户资金
		receipt, err := account.ExecFrozen(action.db, genesis.ReturnAddress, action.execaddr, types.TicketPrice)
		if err != nil {
			tlog.Error("GenesisInit.Frozen", "addr", genesis.ReturnAddress, "execaddr", action.execaddr)
			panic(err)
		}
		t.Save(action.db)
		logs = append(logs, t.GetReceiptLog())
		kv = append(kv, t.GetKVSet()...)
		logs = append(logs, receipt.Logs...)
		kv = append(kv, receipt.KV...)
	}
	receipt := &types.Receipt{types.ExecOk, kv, logs}
	return receipt, nil
}

func saveBind(db dbm.KVDB, tbind *types.TicketBind) {
	set := getBindKV(tbind)
	for i := 0; i < len(set); i++ {
		db.Set(set[i].GetKey(), set[i].Value)
	}
}

func getBindKV(tbind *types.TicketBind) (kvset []*types.KeyValue) {
	value := types.Encode(tbind)
	kvset = append(kvset, &types.KeyValue{TicketBindKey(tbind.ReturnAddress), value})
	return kvset
}

func getBindLog(tbind *types.TicketBind, old string) *types.ReceiptLog {
	log := &types.ReceiptLog{}
	log.Ty = types.TyLogTicketBind
	r := &types.ReceiptTicketBind{}
	r.ReturnAddress = tbind.ReturnAddress
	r.OldMinerAddress = old
	r.NewMinerAddress = tbind.MinerAddress
	log.Log = types.Encode(r)
	return log
}

func (action *TicketAction) getBind(addr string) string {
	value, err := action.db.Get(TicketBindKey(addr))
	if err != nil || value == nil {
		return ""
	}
	var bind types.TicketBind
	err = types.Decode(value, &bind)
	if err != nil {
		panic(err)
	}
	return bind.MinerAddress
}

//授权某个地址进行挖矿
//todo: query address is a minered address
func (action *TicketAction) TicketBind(tbind *types.TicketBind) (*types.Receipt, error) {
	if action.fromaddr != tbind.ReturnAddress {
		return nil, types.ErrFromAddr
	}
	//"" 表示设置为空
	if len(tbind.MinerAddress) > 0 {
		if err := account.CheckAddress(tbind.MinerAddress); err != nil {
			return nil, err
		}
	}
	var logs []*types.ReceiptLog
	var kvs []*types.KeyValue
	saveBind(action.db, tbind)
	kv := getBindKV(tbind)
	oldbind := action.getBind(tbind.ReturnAddress)
	log := getBindLog(tbind, oldbind)
	logs = append(logs, log)
	kvs = append(kvs, kv...)
	receipt := &types.Receipt{types.ExecOk, kvs, logs}
	return receipt, nil
}

func (action *TicketAction) TicketOpen(topen *types.TicketOpen) (*types.Receipt, error) {
	prefix := common.ToHex(action.txhash)
	prefix = topen.MinerAddress + ":" + prefix + ":"
	var logs []*types.ReceiptLog
	var kv []*types.KeyValue
	//addr from
	if action.fromaddr != topen.ReturnAddress {
		mineraddr := action.getBind(topen.ReturnAddress)
		if mineraddr != action.fromaddr {
			return nil, types.ErrMinerNotPermit
		}
		if topen.MinerAddress != mineraddr {
			return nil, types.ErrMinerAddr
		}
	}
	//action.fromaddr == topen.ReturnAddress or mineraddr == action.fromaddr
	for i := 0; i < int(topen.Count); i++ {
		id := prefix + fmt.Sprintf("%010d", i)
		t := NewTicketDB(id, topen.MinerAddress, topen.ReturnAddress, action.blocktime, false)

		//冻结子账户资金
		receipt, err := account.ExecFrozen(action.db, topen.ReturnAddress, action.execaddr, types.TicketPrice)
		if err != nil {
			tlog.Error("TicketOpen.Frozen", "addr", topen.ReturnAddress, "execaddr", action.execaddr, "n", topen.Count)
			return nil, err
		}
		t.Save(action.db)
		logs = append(logs, t.GetReceiptLog())
		kv = append(kv, t.GetKVSet()...)
		logs = append(logs, receipt.Logs...)
		kv = append(kv, receipt.KV...)
	}
	receipt := &types.Receipt{types.ExecOk, kv, logs}
	return receipt, nil
}

func readTicket(db dbm.KVDB, id string) (*types.Ticket, error) {
	data, err := db.Get(TicketKey(id))
	if err != nil {
		return nil, err
	}
	var ticket types.Ticket
	//decode
	err = types.Decode(data, &ticket)
	if err != nil {
		return nil, err
	}
	return &ticket, nil
}

func (action *TicketAction) TicketMiner(miner *types.TicketMiner, index int) (*types.Receipt, error) {
	if index != 0 {
		return nil, types.ErrCoinBaseIndex
	}
	ticket, err := readTicket(action.db, miner.TicketId)
	if err != nil {
		return nil, err
	}
	if ticket.Status != 1 {
		return nil, types.ErrCoinBaseTicketStatus
	}
	if !ticket.IsGenesis {
		if action.blocktime-ticket.GetCreateTime() < types.TicketFrozenTime {
			return nil, types.ErrTime
		}
	}
	//check from address
	if action.fromaddr != ticket.MinerAddress && action.fromaddr != ticket.ReturnAddress {
		return nil, types.ErrFromAddr
	}
	prevstatus := ticket.Status
	ticket.Status = 2
	ticket.MinerValue = miner.Reward
	t := &TicketDB{*ticket, prevstatus}
	var logs []*types.ReceiptLog
	var kv []*types.KeyValue

	//user
	receipt1, err := account.ExecDepositFrozen(action.db, t.ReturnAddress, action.execaddr, ticket.MinerValue)
	if err != nil {
		tlog.Error("TicketMiner.ExecDepositFrozen user", "addr", t.ReturnAddress, "execaddr", action.execaddr)
		return nil, err
	}
	//fund
	receipt2, err := account.ExecDepositFrozen(action.db, types.FundKeyAddr, action.execaddr, types.CoinDevFund)
	if err != nil {
		tlog.Error("TicketMiner.ExecDepositFrozen fund", "addr", types.FundKeyAddr, "execaddr", action.execaddr)
		return nil, err
	}
	t.Save(action.db)
	logs = append(logs, t.GetReceiptLog())
	kv = append(kv, t.GetKVSet()...)
	logs = append(logs, receipt1.Logs...)
	kv = append(kv, receipt1.KV...)
	logs = append(logs, receipt2.Logs...)
	kv = append(kv, receipt2.KV...)
	return &types.Receipt{types.ExecOk, kv, logs}, nil
}

func (action *TicketAction) TicketClose(tclose *types.TicketClose) (*types.Receipt, error) {
	tickets := make([]*TicketDB, len(tclose.TicketId))
	for i := 0; i < len(tclose.TicketId); i++ {
		ticket, err := readTicket(action.db, tclose.TicketId[i])
		if err != nil {
			return nil, err
		}
		//ticket 的生成时间超过 2天,可提款
		if ticket.Status != 2 && ticket.Status != 1 {
			tlog.Error("ticket", "id", ticket.GetTicketId(), "status", ticket.GetStatus())
			return nil, types.ErrTicketClosed
		}
		if !ticket.IsGenesis {
			//分成两种情况
			if ticket.Status == 1 && action.blocktime-ticket.GetCreateTime() < types.TicketWithdrawTime {
				return nil, types.ErrTime
			}
			//已经挖矿成功了
			if ticket.Status == 2 && action.blocktime-ticket.GetCreateTime() < types.TicketWithdrawTime {
				return nil, types.ErrTime
			}
			if ticket.Status == 2 && action.blocktime-ticket.GetMinerTime() < types.TicketMinerWaitTime {
				return nil, types.ErrTime
			}
		}
		//check from address
		if action.fromaddr != ticket.MinerAddress && action.fromaddr != ticket.ReturnAddress {
			return nil, types.ErrFromAddr
		}
		prevstatus := ticket.Status
		ticket.Status = 3
		tickets[i] = &TicketDB{*ticket, prevstatus}
	}
	var logs []*types.ReceiptLog
	var kv []*types.KeyValue
	for i := 0; i < len(tickets); i++ {
		t := tickets[i]
		if t.prevstatus == 1 {
			t.MinerValue = 0
		}
		retValue := types.TicketPrice + t.MinerValue
		receipt1, err := account.ExecActive(action.db, t.ReturnAddress, action.execaddr, retValue)
		if err != nil {
			tlog.Error("TicketClose.ExecActive user", "addr", t.ReturnAddress, "execaddr", action.execaddr, "value", retValue)
			return nil, err
		}
		logs = append(logs, t.GetReceiptLog())
		kv = append(kv, t.GetKVSet()...)
		logs = append(logs, receipt1.Logs...)
		kv = append(kv, receipt1.KV...)
		//如果ticket 已经挖矿成功了，那么要解冻发展基金部分币
		if t.prevstatus == 2 {
			receipt2, err := account.ExecActive(action.db, types.FundKeyAddr, action.execaddr, types.CoinDevFund)
			if err != nil {
				tlog.Error("TicketClose.ExecActive fund", "addr", types.FundKeyAddr, "execaddr", action.execaddr, "value", retValue)
				return nil, err
			}
			logs = append(logs, receipt2.Logs...)
			kv = append(kv, receipt2.KV...)
		}
		t.Save(action.db)
	}
	receipt := &types.Receipt{types.ExecOk, kv, logs}
	return receipt, nil
}

func TicketList(db dbm.DB, db2 dbm.KVDB, tlist *types.TicketList) (types.Message, error) {
	values := db.List(calcTicketPrefix(tlist.Addr, tlist.Status), nil, 0, 0)
	if len(values) == 0 {
		return &types.ReplyTicketList{}, nil
	}
	var ids types.TicketInfos
	for i := 0; i < len(values); i++ {
		ids.TicketIds = append(ids.TicketIds, string(values[i]))
	}
	return TicketInfos(db2, &ids)
}

func TicketInfos(db dbm.KVDB, tinfos *types.TicketInfos) (types.Message, error) {
	var tickets []*types.Ticket
	for i := 0; i < len(tinfos.TicketIds); i++ {
		id := tinfos.TicketIds[i]
		ticket, err := readTicket(db, id)
		if err != nil {
			return nil, err
		}
		tickets = append(tickets, ticket)
	}
	return &types.ReplyTicketList{tickets}, nil
}