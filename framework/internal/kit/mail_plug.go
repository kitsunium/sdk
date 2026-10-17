package kit

import (
	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/mail"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// kit's mailers through the mail subsystem (ADR 0179 §1, step 6): the core
// opens every mailer's outbox — the SDK's mail spool over a transport kit
// runs each attempt through — with a plug.MailSpoolConfig and holds it as a
// plug.MailSpool, and hands its mail to the plug.MailSink the subsystem
// builds, SMTP or capture; framework/kit/mail plugs them in. The core names
// the SDK's mail types, which Mailer.Send takes, and imports neither its
// spool nor a transport.

// mailPackage is the package a program imports for its mailers.
const mailPackage = "github.com/kitsunium/sdk/framework/kit/mail"

// mailsMissing is the refusal of a mailer in a program that does not import
// the mail subsystem.
func mailsMissing() error {
	return errs.New(CodeSubsystemMissing, "SUBSYSTEM_NOT_IMPORTED", subsystemPublic,
		"kit: a mailer needs the mail subsystem, which the program does not import: "+mailPackage,
		errs.String("packages", mailPackage))
}

// mailsLinked reports whether the program imports framework/kit/mail.
func mailsLinked() bool { return plug.Mail.Load() != nil }

// validateMail is the SDK's verdict on msg, through the mail subsystem: the
// refusal naming its import without it.
func validateMail(msg mail.Message) error {
	d := plug.Mail.Load()
	if d == nil || d.Validate == nil {
		return mailsMissing()
	}
	return d.Validate(plug.MailValue{Value: msg})
}

// mailRefused reports whether err is the mail subsystem's refusal of kind:
// the SDK's sentinels stay in the subsystem, which tells them apart. A
// mailer runs only with the subsystem, so without it no error is one of its
// refusals.
func mailRefused(err error, kind plug.MailRefusal) bool {
	d := plug.Mail.Load()
	return err != nil && d != nil && d.Refused != nil && d.Refused(err, kind)
}
