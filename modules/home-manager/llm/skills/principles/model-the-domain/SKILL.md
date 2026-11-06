---
name: model-the-domain
description: Use when choosing the types, fields and state a change keeps, before writing the logic that uses them.
---

# Model the domain

Name the data's shape before the logic: the states a thing can be in and
the moves between them, as one type. A second boolean kept in step with a
first is a state machine: one type with a value per state.

Give control information its own channel. In-band signaling carries it in
the same type as valid data, so a data value can be mistaken for a signal
and a signal for data: a password field where the string `"*"` means "no
password" accepts the password `*` and cannot tell it from the signal;
likewise `-1` for "not found", `""` for "absent", an in-text delimiter or
escape, a magic timestamp. Use a variant (`NoPassword | Hash(h)`), an
option, a result type or a separate field, so the compiler separates the
signal from the data and no data value can forge it.

**The test:** each combination of values the type allows is a state the
domain has, and each state the domain has is one value; no value of a data
type means anything but data.

You skipped this when code checks for combinations of flags that should
never occur, or a comment says which fields go together, or a comment says
which value of a field means something else, or data is compared to a
literal before it is used as data.
