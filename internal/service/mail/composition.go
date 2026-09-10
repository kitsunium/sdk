// Package mail — the per-message build state, and the MIME structure it
// assembles.
package mail

import (
	"encoding/hex"
	"io"
	"maps"
	"strconv"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// composition is the per-message state the boundary generator needs: one random
// token, and a counter that makes every container in this message distinct from
// every other.
//
// It exists rather than living on the [Composer] because a Composer is shared
// and safe for concurrent use, so a counter on it would either race or be
// non-deterministic — and determinism is what lets a consumer golden-test its
// own templates.
type composition struct {
	*Composer
	token   string
	counter int
}

// newComposition draws the one random token this message's boundaries share.
func (c *Composer) newComposition() (builder *composition, err error) {
	var token [boundaryTokenOctets]byte
	//: one read per message rather than one per container: fewer syscalls, and
	//: distinctness comes from the counter rather than from the draw.
	if _, readErr := io.ReadFull(c.random, token[:]); readErr != nil {
		//: a randomness source that cannot produce its bytes is not one this
		//: composer will guess around.
		return nil, wrapAs(ComposeFailed, readErr, errs.String("stage", "boundary"))
	}
	//: ready to build.
	return &composition{Composer: c, token: hex.EncodeToString(token[:])}, nil
}

// newBoundary returns a delimiter that cannot collide with anything in the
// message — which is a structural claim here and not a probabilistic one.
//
// Two things have to be impossible. A boundary must not appear inside a part's
// content: every leaf this composer emits is quoted-printable or base64, and
// both alphabets reach "=" only as an escape prefix, a soft line break or
// trailing padding, so the literal two octets "=_" cannot occur in either — and
// every boundary starts with them. And two boundaries in one message must
// differ: they share one random token and differ by a counter, so they are
// pairwise distinct by construction rather than by luck.
//
// TestBoundaryCannotAppearInAnyEncodedBody and
// TestNestedBoundariesAreAlwaysDistinct are the guards; the second runs with a
// randomness source that returns a CONSTANT, so a design relying on the draw
// would fail every time.
func (c *composition) newBoundary() string {
	//: the counter is what makes nesting safe.
	c.counter++
	//: "=_" is the prefix no encoded body can contain.
	return "=_" + c.token + "_" + strconv.Itoa(c.counter)
}

// buildBody assembles the MIME entity tree for msg.
func (c *composition) buildBody(msg coremail.MessageValue) (root entity, err error) {
	inlines, files := splitAttachments(msg.Attachments)
	body, bodyErr := c.buildBodyRoot(msg)
	//: ComposeFailed.
	if bodyErr != nil {
		//: no tree to render.
		return entity{}, bodyErr
	}
	//: inline parts wrap the body in a multipart/related, because a cid:
	//: reference resolves only among the parts of the SAME related container
	//: (RFC 2387 §3).
	if len(inlines) > 0 {
		related, relatedErr := c.buildRelated(body, inlines)
		//: ComposeFailed.
		if relatedErr != nil {
			//: no tree to render.
			return entity{}, relatedErr
		}
		body = related
	}
	//: attachments wrap whatever that produced in a multipart/mixed, which is
	//: the outermost container by RFC 2046 §5.1.3.
	if len(files) > 0 {
		//: mixed on the outside, always.
		return c.buildMixed(body, files)
	}
	//: no container needed.
	return body, nil
}

// buildBodyRoot returns the entity that holds the human-readable body: one
// leaf, or the multipart/alternative that carries both forms.
func (c *composition) buildBodyRoot(msg coremail.MessageValue) (root entity, err error) {
	plain := textEntity(typeTextPlain, msg.Text)
	html := textEntity(typeTextHTML, msg.HTML)
	//: three shapes, decided by which of the two bodies are populated.
	switch {
	//: both forms: the alternative, plain FIRST (RFC 2046 §5.1.4).
	case msg.Text != "" && msg.HTML != "":
		//: two children, one container.
		return c.multipart("multipart/alternative", nil, []entity{plain, html})
	//: HTML alone. No plain text is invented for it: producing one means
	//: stripping tags, which is a lossy transformation the caller did not ask
	//: for and cannot review.
	case msg.HTML != "":
		//: one leaf.
		return html, nil
	//: text alone, and also the attachments-only case — an empty text/plain
	//: part is what keeps a mail with only a file from rendering as a message
	//: with no body at all.
	default:
		//: one leaf.
		return plain, nil
	}
}

// buildRelated wraps the body and its inline parts in a multipart/related.
func (c *composition) buildRelated(
	body entity, inlines []coremail.AttachmentValue,
) (related entity, err error) {
	children := make([]entity, 0, len(inlines)+1)
	//: the root part comes first, which is what lets the "start" parameter be
	//: omitted (RFC 2387 §3.2 defaults it to the first part).
	children = append(children, body)
	//: then every inline part, in the caller's order.
	for index := range inlines {
		part, partErr := attachmentEntity(&inlines[index], index)
		//: ComposeFailed.
		if partErr != nil {
			//: no container to build.
			return entity{}, partErr
		}
		children = append(children, part)
	}
	//: the "type" parameter is REQUIRED on multipart/related (RFC 2387 §3.1)
	//: and names the root part's media type.
	params := map[string]string{"type": mediaTypeOnly(body.contentType)}
	//: the container.
	return c.multipart("multipart/related", params, children)
}

// buildMixed wraps the body and the file attachments in a multipart/mixed.
func (c *composition) buildMixed(
	body entity, files []coremail.AttachmentValue,
) (mixed entity, err error) {
	children := make([]entity, 0, len(files)+1)
	//: the body first, so a client with no attachment support still shows it.
	children = append(children, body)
	//: then every file, in the caller's order.
	for index := range files {
		part, partErr := attachmentEntity(&files[index], index)
		//: ComposeFailed.
		if partErr != nil {
			//: no container to build.
			return entity{}, partErr
		}
		children = append(children, part)
	}
	//: the container.
	return c.multipart("multipart/mixed", nil, children)
}

// multipart builds a container entity with a fresh boundary.
func (c *composition) multipart(
	subtype string, params map[string]string, children []entity,
) (container entity, err error) {
	full := map[string]string{"boundary": c.newBoundary()}
	//: any subtype-specific parameter, e.g. multipart/related's "type".
	maps.Copy(full, params)
	rendered, formatErr := formatMediaType(subtype, full)
	//: ComposeFailed.
	if formatErr != nil {
		//: unformattable.
		return entity{}, formatErr
	}
	//: a multipart carries no Content-Transfer-Encoding: RFC 2045 §6.4 permits
	//: only 7bit/8bit/binary there, and 7bit is already the default.
	return entity{contentType: rendered, boundary: full["boundary"], children: children}, nil
}
