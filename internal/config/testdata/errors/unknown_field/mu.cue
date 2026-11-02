// Every schema definition is closed, so a misspelled field is an error
// rather than silently ignored — here, a typo that used to leave the
// secret write allow-list unset. A close match earns a did-you-mean.
secrets: writable_ref: ["pass:bootstrap/*"]
