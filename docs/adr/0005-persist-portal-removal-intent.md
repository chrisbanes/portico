# Persist Portal removal intent before deleting identity state

Before Portico permanently deletes local Portal identity state, it commits a
durable removal lifecycle that prevents the Portal from running and lets
cleanup resume safely after interruption. The Portal record is removed only
after guarded deletion of its UUID-keyed state succeeds; remote tailnet-node
cleanup remains the manual process established by ADR 0003.
