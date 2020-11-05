// (c) 2019-2020, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package conflicts

import (
	"errors"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow/choices"
)

var (
	errInvalidTxType = errors.New("invalid tx type")
)

type Conflicts struct {
	// track the currently processing txs
	txs map[[32]byte]Tx

	// track which txs are currently consuming which utxos
	consumedUTXOs map[[32]byte]ids.Set

	// track which txs are currently producing which utxos
	producedUTXOs map[[32]byte]ids.Set

	// track txs that have been marked as ready to accept
	acceptable []choices.Decidable

	// track txs that have been marked as ready to reject
	rejectable []choices.Decidable
}

func New() *Conflicts {
	return &Conflicts{
		txs:           make(map[[32]byte]Tx),
		consumedUTXOs: make(map[[32]byte]ids.Set),
		producedUTXOs: make(map[[32]byte]ids.Set),
	}
}

// Add this tx to the conflict set. If this tx is of the correct type, this tx
// will be added to the set of processing txs. It is assumed this tx wasn't
// already processing. This will mark the consumed utxos and register a rejector
// that will be notified if a dependency of this tx was rejected.
func (c *Conflicts) Add(txIntf choices.Decidable) error {
	tx, ok := txIntf.(Tx)
	if !ok {
		return errInvalidTxType
	}

	txID := tx.ID()
	c.txs[txID.Key()] = tx
	for inputKey := range tx.InputIDs() {
		spenders := c.consumedUTXOs[inputKey]
		spenders.Add(txID)
		c.consumedUTXOs[inputKey] = spenders
	}
	for outputKey := range tx.OutputIDs() {
		producers := c.producedUTXOs[outputKey]
		producers.Add(txID)
		c.producedUTXOs[outputKey] = producers
	}
	return nil
}

// IsVirtuous checks the currently processing txs for conflicts. It is allowed
// to call with function with txs that aren't yet processing or currently
// processing txs.
func (c *Conflicts) IsVirtuous(txIntf choices.Decidable) (bool, error) {
	tx, ok := txIntf.(Tx)
	if !ok {
		return false, errInvalidTxType
	}

	txID := tx.ID()
	for inputKey := range tx.InputIDs() {
		spenders := c.utxos[inputKey]
		if numSpenders := spenders.Len(); numSpenders > 1 ||
			(numSpenders == 1 && !spenders.Contains(txID)) {
			return false, nil
		}
	}
	return true, nil
}

// Conflicts returns the collection of txs that are currently processing that
// conflict with the provided tx. It is allowed to call with function with txs
// that aren't yet processing or currently processing txs.
func (c *Conflicts) Conflicts(txIntf choices.Decidable) ([]choices.Decidable, error) {
	tx, ok := txIntf.(Tx)
	if !ok {
		return nil, errInvalidTxType
	}

	txID := tx.ID()
	var conflictSet ids.Set
	conflictSet.Add(txID)

	conflicts := []choices.Decidable(nil)
	for inputKey := range tx.InputIDs() {
		spenders := c.utxos[inputKey]
		for spenderKey := range spenders {
			spenderID := ids.NewID(spenderKey)
			if conflictSet.Contains(spenderID) {
				continue
			}
			conflictSet.Add(spenderID)
			conflicts = append(conflicts, c.txs[spenderKey])
		}
	}
	return conflicts, nil
}

// Accept notifies this conflict manager that a tx has been conditionally
// accepted. This means that assuming all the txs this tx depends on are
// accepted, then this tx should be accepted as well. This
func (c *Conflicts) Accept(txID ids.ID) {
	tx, exists := c.txs[txID.Key()]
	if !exists {
		return
	}

	toAccept := &acceptor{
		c:  c,
		tx: tx,
	}
	for _, dependency := range tx.Dependencies() {
		if dependency.Status() != choices.Accepted {
			// If the dependency isn't accepted, then it must be processing.
			// This tx should be accepted after this tx is accepted. Note that
			// the dependencies can't already be rejected, because it is assumed
			// that this tx is currently considered valid.
			toAccept.deps.Add(dependency.ID())
		}
	}
	c.pendingAccept.Register(toAccept)
}

func (c *Conflicts) Updateable() ([]choices.Decidable, []choices.Decidable) {
	acceptable := c.acceptable
	c.acceptable = nil
	for _, tx := range acceptable {
		txID := tx.ID()

		tx := tx.(Tx)
		for inputKey := range tx.InputIDs() {
			spenders := c.utxos[inputKey]
			spenders.Remove(txID)
			if spenders.Len() == 0 {
				delete(c.utxos, inputKey)
			} else {
				c.utxos[inputKey] = spenders
			}
		}

		delete(c.txs, txID.Key())
		c.pendingAccept.Fulfill(txID)
		c.pendingReject.Abandon(txID)

		// Conflicts should never return an error, as the type has already been
		// asserted.
		conflicts, _ := c.Conflicts(tx)
		for _, conflict := range conflicts {
			c.pendingReject.Fulfill(conflict.ID())
		}
	}

	rejectable := c.rejectable
	c.rejectable = nil
	for _, tx := range rejectable {
		txID := tx.ID()

		tx := tx.(Tx)
		for inputKey := range tx.InputIDs() {
			spenders := c.utxos[inputKey]
			spenders.Remove(txID)
			if spenders.Len() == 0 {
				delete(c.utxos, inputKey)
			} else {
				c.utxos[inputKey] = spenders
			}
		}

		delete(c.txs, txID.Key())
		c.pendingAccept.Abandon(txID)
		c.pendingReject.Fulfill(txID)
	}

	return acceptable, rejectable
}
