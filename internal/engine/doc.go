// Package engine implements the in-memory game engine using the actor pattern.
//
// One Session per active game owns that game's state. A goroutine inside the
// Session is the only code that reads or writes the state directly — all
// external interactions go through the inbox channel via Submit.
//
// The Router maps game IDs to Sessions and handles lazy loading from the
// store (latest snapshot + subsequent events) when a command arrives for
// a game not currently in memory. Idle sessions are evicted to free memory;
// the next command for that game triggers a reload.
//
// This file exists to put the package's mental model in one place. The
// pattern is simple but the consequences are everywhere:
//
//   - No mutex on GameState. The Session's goroutine is the only accessor.
//   - Submit is safe to call concurrently. Calls are serialized inside the
//     Session's run loop.
//   - The handler (events from command) and reducer (new state from events)
//     are pure functions. The Session orchestrates persistence around them.
package engine