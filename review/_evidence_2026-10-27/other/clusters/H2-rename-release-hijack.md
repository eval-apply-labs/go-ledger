# H2-rename-release-hijack (new hypothesis from the lead reviewer — NOT yet tested)

**Claim.** With a stolen account key, a thief can take the victim's registered name WITHOUT moving any money (so a balance-watching owner sees nothing):
1. Sign an AccountUpdate for the VICTIM's account (From = victim key) setting `Name` to some other free name. In `applyAccountUpdates` (ledger/account_update.go ~83-114) a rename deletes the old name from `ownerByName` ("release the previous name") and claims the new one.
2. In the same block (ordered after step 1) or the next block, an AccountUpdate from the thief's own funded account claims the victim's old name — it is now free.
3. Pay-by-name payments resolve on-chain at apply time, so every payment to that name — including ones already signed for future BlockIDs — credits the thief. No ⅔ governance action exists today that can move a name back (reassignment only sets balances).

**What to establish:**
- A ledger-level PoC (ProposeBlock/ApplyBlock or applyAccountUpdates directly, mirroring how blocks carry updates) showing the name moves to the thief and a subsequent pay-by-name payment credits the thief while the victim's balance is unchanged.
- How in-block update ordering is determined in practice (node update pool order — node/updatepool.go; ProposeBlock keeps content order) and whether the thief can make it deterministic simply by using two consecutive blocks.
- Whether any intake/node guard blocks a rename of an already-named account.
- Confirm the owner's DECIDED fix closes it: "an AccountUpdate may claim a name only if the account has none; renames/moves/sales go through a ⅔ name-reassignment vote", together with "a named account is never deleted by a balance event (zero balance, overdraw ban, slash)". Point out any remaining path by which a name can change owner without a vote.
