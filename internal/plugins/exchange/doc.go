// Package exchange maps GoModel's core request and response types to the
// unified pluginapi types plugins see, and applies plugin edits back.
//
// Message identity: messages built from a request get IDs "m<index>" (the
// 0-based position in the original message or input list); a Responses
// instructions field becomes the message with ID "instructions"; completion
// choices are keyed "choice:<index>". Apply-back consults Prompt.Changes and
// Completion.Changes: untouched messages are copied from the original typed
// value verbatim, edited ones have only the touched parts rewritten inside
// their original structure, inserted ones are encoded from the unified form,
// and removed ones are dropped.
package exchange
