package cache

import (
	"slices"
	"strconv"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// absentTTLField names the field the negative lifetime rides on.
const absentTTLField string = "absent_ttl_ns"

// ttlBase and ttlBits parse the field back: a decimal int64.
const (
	ttlBase int = 10
	ttlBits int = 64
)

// Absent marks a fill's failure as an ANSWER to cache: the key does not exist
// at the origin, and asking again before ttl has passed would get the same
// answer. A store whose Load receives it stores a negative entry for ttl;
// until it expires, Load returns this same error — errors.Is still finds
// cause — and calls no fill, while Fetch reports a miss and a Set replaces
// it.
//
//	row, err := db.Get(ctx, id)
//	if errors.Is(err, sql.ErrNoRows) {
//		return cache.EntryValue[Row]{}, cache.Absent(err, time.Minute)
//	}
//
// The mark is an errs.Wrap onto cause: an SDK cause keeps its own Code,
// Reason and Public, [CodeCacheAbsent] joins its wrap trail and the lifetime
// rides as a field; any other cause records [CacheAbsent] itself. A ttl that
// is not positive caches nothing: the failure is returned as any other.
func Absent(cause error, ttl time.Duration) error {
	//: ONE call for both paths, by errs.Wrap's own rule, as the queue's
	//: DoNotRetry: an SDK cause stays the origin and takes only the Code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CacheAbsent.Code(),
		Reason:  CacheAbsent.Reason(),
		Public:  CacheAbsent.Public(),
		Private: CacheAbsent.Private(),
	}, errs.Int64(absentTTLField, int64(ttl)))
}

// AbsentTTLOf returns the lifetime [Absent] put on err, and whether it put
// one. When err was marked more than once, the outermost mark's lifetime is
// returned.
func AbsentTTLOf(err error) (ttl time.Duration, marked bool) {
	//: the code is the mark; a field of the same name without it is somebody
	//: else's.
	if !errs.HasCode(err, CodeCacheAbsent) {
		//: not marked.
		return 0, false
	}
	//: every layer's fields, inner first, so the last match is the outermost
	//: mark's.
	for _, field := range slices.Backward(errs.FieldsOf(err)) {
		//: another field.
		if field.Key() != absentTTLField {
			continue
		}
		nanos, parseErr := strconv.ParseInt(field.StringValue(), ttlBase, ttlBits)
		//: a field of that name that is not Absent's integer: nothing to keep.
		if parseErr != nil {
			//: marked, with no lifetime: cache nothing.
			return 0, true
		}
		//: the lifetime asked for.
		return time.Duration(nanos), true
	}
	//: marked, but the field did not travel: cache nothing.
	return 0, true
}
