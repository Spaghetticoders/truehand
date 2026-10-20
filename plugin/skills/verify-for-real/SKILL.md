---
name: verify-for-real
description: The rituals that find real defects — break the fix and watch the test fail, inject the fault instead of deducing it, count instead of estimating. Use before saying anything is fixed, correct or verified.
---

# Verify for real

The difference between "I fixed it" and "I verified it".

## 1. Break the fix, watch the test fail

A test that passes on broken code proves nothing. `truehand_test_proof` does this for the
whole change; do it by hand for each individual fix too: restore the old behavior, run the
tests, **see them fail**, put the fix back. A test can pass for the wrong reason — a fixture
missing the field the filter needs makes the count zero and the assertion true by accident.

## 2. Inject the fault, do not deduce it

When the conclusion is "if the network fails, X happens", replace the response with an error,
rebuild, and look. Deductions from code are wrong more often than they feel. A counter that
fails only the first attempt also shows the recovery. Remove the probe afterwards and grep
for its marker.

## 3. Count, do not estimate

"It seems to run two or three times" is not a measure. Count it — and beware of double
counting: network loggers print request and response.

## 4. Cross-check with an external source

A screen consistent with itself proves nothing. Compare with something outside the app: the
design node (`truehand_eyes`), the backend response, the network log.

## 5. Enumerate the states, do not patch the symptom

If you got the same boundary wrong twice, you are fixing the case you just saw. List all the
states and cover them: the question is not "is this case covered?" but "what are all the
cases?".
