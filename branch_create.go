package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/charmbracelet/log"
	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/state"
)

type branchCreateCmd struct {
	Name string `arg:"" optional:"" help:"Name of the new branch"`

	Insert bool `help:"Restack the upstack of the current branch on top of the new branch"`
	Below  bool `help:"Place the branch below the current branch. Implies --insert."`

	Message string `short:"m" long:"message" optional:"" help:"Commit message"`
}

func (cmd *branchCreateCmd) Run(ctx context.Context, log *log.Logger) (err error) {
	repo, err := git.Open(ctx, ".", git.OpenOptions{
		Log: log,
	})
	if err != nil {
		return fmt.Errorf("open repository: %w", err)
	}

	store, err := ensureStore(ctx, repo, log)
	if err != nil {
		return err
	}
	trunk := store.Trunk()

	// TODO: guess branch name from commit subject
	if cmd.Name == "" {
		return errors.New("branch name is required")
	}

	currentBranch, err := repo.CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("get current branch: %w", err)
	}

	currentHash, err := repo.PeelToCommit(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("peel to tree: %w", err)
	}

	diff, err := repo.DiffIndex(ctx, currentHash.String())
	if err != nil {
		return fmt.Errorf("diff index: %w", err)
	}

	base := &state.BranchBase{
		Name: currentBranch,
		Hash: currentHash,
	}

	// Branches to restack on top of new branch.
	var restackOntoNew []string
	if cmd.Below {
		if currentBranch == trunk {
			log.Error("--below: cannot create a branch below trunk")
			return fmt.Errorf("--below cannot be used from  %v", trunk)
		}

		b, err := store.LookupBranch(ctx, currentBranch)
		if err != nil {
			return fmt.Errorf("branch not tracked: %v", currentBranch)
		}

		// If trying to insert below current branch,
		// detach to base instead,
		// and restack current branch on top.
		base = b.Base
		restackOntoNew = append(restackOntoNew, currentBranch)
	} else if cmd.Insert {
		// If inserting, restacking all the upstacks of current branch
		// onto the new branch.
		aboves, err := store.ListAbove(ctx, currentBranch)
		if err != nil {
			return fmt.Errorf("list branches above %s: %w", currentBranch, err)
		}

		restackOntoNew = append(restackOntoNew, aboves...)
	}

	if err := repo.DetachHead(ctx, base.Name); err != nil {
		return fmt.Errorf("detach head: %w", err)
	}
	// From this point on, if there's an error,
	// restore the original branch.
	defer func() {
		if err != nil {
			err = errors.Join(err, repo.Checkout(ctx, currentBranch))
		}
	}()

	if err := repo.Commit(ctx, git.CommitRequest{
		AllowEmpty: len(diff) == 0,
		Message:    cmd.Message,
	}); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	if err := repo.CreateBranch(ctx, git.CreateBranchRequest{
		Name: cmd.Name,
		Head: "HEAD",
	}); err != nil {
		return fmt.Errorf("create branch: %w", err)
	}

	if err := repo.Checkout(ctx, cmd.Name); err != nil {
		return fmt.Errorf("checkout branch: %w", err)
	}

	var upserts []state.UpsertBranchRequest
	upserts = append(upserts, state.UpsertBranchRequest{
		Name:     cmd.Name,
		Base:     base.Name,
		BaseHash: base.Hash,
	})

	for _, branch := range restackOntoNew {
		// For --insert and --below, set the base branch of all affected
		// branches to the newly created branch and run a restack.
		upserts = append(upserts, state.UpsertBranchRequest{
			Name: branch,
			Base: cmd.Name,
		})
	}

	var msg string
	if cmd.Below {
		msg = fmt.Sprintf("insert branch %s below %s", cmd.Name, base.Name)
	} else if cmd.Insert {
		msg = fmt.Sprintf("insert branch %s above %s", cmd.Name, base.Name)
	} else {
		msg = fmt.Sprintf("create branch %s", cmd.Name)
	}

	if err := store.UpsertBranches(ctx, upserts, msg); err != nil {
		return fmt.Errorf("update state: %w", err)
	}

	if cmd.Below || cmd.Insert {
		return (&upstackRestackCmd{}).Run(ctx, log)
	}

	return nil
}
