package core

import (
	"context"

	"github.com/filecoin-project/go-filecoin/chain"
	"github.com/filecoin-project/go-filecoin/types"
)

// CollectBlocks collects blocks by traversing the chain from a tipset towards its parents, until some
// minimum height (exclusive).
// Returns the blocks collected and a tipset iterator positioned at the tipset at `endHeight`
func CollectBlocks(ctx context.Context, store chain.BlockProvider, head types.TipSet, endHeight uint64) ([]*types.Block, *chain.TipsetIterator, error) {
	var blocks []*types.Block
	var err error
	tsItr := chain.IterAncestors(ctx, store, head)
	for ; err == nil && !tsItr.Complete(); err = tsItr.Next() {
		ts := tsItr.Value()
		height, err := ts.Height()
		if err != nil || height <= endHeight {
			break
		}
		for _, b := range ts {
			blocks = append(blocks, b)
		}
	}
	return blocks, tsItr, err
}

// CollectBlocksToCommonAncestor traverses chains from two tipsets (called old and new) until their common
// ancestor, collecting all blocks that are in one chain but not the other.
func CollectBlocksToCommonAncestor(ctx context.Context, store chain.BlockProvider, oldHead, newHead types.TipSet) (oldBlocks, newBlocks []*types.Block, err error) {
	// Strategy: walk head-of-chain pointers old and new back until they are at the same height,
	// then walk back in lockstep to find the common ancestor.

	// If old is higher than new, collect all the messages from the old chain down to the height of new (exclusive).
	newHeight, err := newHead.Height()
	if err != nil {
		return
	}
	oldBlocks, oldItr, err := CollectBlocks(ctx, store, oldHead, newHeight)
	if err != nil {
		return
	}

	// If new is higher than old, collect all the messages from new's chain down to the height of old.
	oldHeight, err := oldHead.Height()
	if err != nil {
		return
	}
	newBlocks, newItr, err := CollectBlocks(ctx, store, newHead, oldHeight)
	if err != nil {
		return
	}

	// The tipset iterators are now at the same height.
	// Continue traversing tipsets in lockstep until they reach the common ancestor.
	for !(oldItr.Complete() || newItr.Complete() || oldItr.Value().Equals(newItr.Value())) {
		for _, b := range oldItr.Value() {
			oldBlocks = append(oldBlocks, b)
		}
		for _, b := range newItr.Value() {
			newBlocks = append(newBlocks, b)
		}

		// Advance iterators
		if err = oldItr.Next(); err != nil {
			return
		}
		if err = newItr.Next(); err != nil {
			return
		}
	}
	return
}
