package deliverability

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// SMTPVerifyResult is layer-2 mailbox existence probe (no DATA).
type SMTPVerifyResult struct {
	Attempted bool
	Accept    bool // 250 on RCPT
	Reject    bool // 550-class
	Code      int
	Detail    string
}

// VerifySMTP performs HELO/MAIL FROM/RCPT TO without sending.
// Many providers disable this — treat inconclusive as soft signal only.
func VerifySMTP(ctx context.Context, mxHost, fromEmail, toEmail string) SMTPVerifyResult {
	res := SMTPVerifyResult{Attempted: true}
	if mxHost == "" {
		res.Detail = "no mx"
		return res
	}
	d := net.Dialer{Timeout: 4 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(mxHost, "25"))
	if err != nil {
		res.Detail = err.Error()
		return res
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	r := bufio.NewReader(conn)
	if code, msg, err := readSMTP(r); err != nil || code != 220 {
		res.Detail = fmt.Sprintf("banner %d %s %v", code, msg, err)
		return res
	}
	helo := "outreachcrm.local"
	if _, domain, ok := SplitEmail(fromEmail); ok && domain != "" {
		helo = domain
	}
	if err := writeSMTP(conn, "HELO "+helo); err != nil {
		res.Detail = err.Error()
		return res
	}
	if code, msg, err := readSMTP(r); err != nil || code >= 400 {
		res.Detail = fmt.Sprintf("helo %d %s %v", code, msg, err)
		return res
	}
	mailFrom := fromEmail
	if mailFrom == "" {
		mailFrom = "probe@" + helo
	}
	if err := writeSMTP(conn, "MAIL FROM:<"+mailFrom+">"); err != nil {
		res.Detail = err.Error()
		return res
	}
	if code, msg, err := readSMTP(r); err != nil || code >= 400 {
		res.Detail = fmt.Sprintf("mail from %d %s %v", code, msg, err)
		return res
	}
	if err := writeSMTP(conn, "RCPT TO:<"+toEmail+">"); err != nil {
		res.Detail = err.Error()
		return res
	}
	code, msg, err := readSMTP(r)
	res.Code = code
	res.Detail = msg
	if err != nil {
		res.Detail = err.Error()
		return res
	}
	_ = writeSMTP(conn, "QUIT")
	if code >= 200 && code < 300 {
		res.Accept = true
		return res
	}
	if code >= 550 && code <= 553 {
		res.Reject = true
		return res
	}
	return res
}

func writeSMTP(conn net.Conn, line string) error {
	_, err := fmt.Fprintf(conn, "%s\r\n", line)
	return err
}

func readSMTP(r *bufio.Reader) (int, string, error) {
	var last string
	var code int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return code, last, err
		}
		line = strings.TrimRight(line, "\r\n")
		last = line
		if len(line) < 3 {
			continue
		}
		fmt.Sscanf(line[:3], "%d", &code)
		if len(line) == 3 || line[3] == ' ' {
			return code, line, nil
		}
		// multiline (-) continue
	}
}
