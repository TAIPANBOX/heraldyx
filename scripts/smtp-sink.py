#!/usr/bin/env python3
"""A minimal SMTP server that accepts one session and prints exactly what it got.

Not a mail server: it speaks the smallest dialect a client needs (EHLO, MAIL
FROM, RCPT TO, DATA, QUIT) and writes the session plus the message to stdout.
It exists to answer one question that no unit test can: does heraldyx's SMTP
client actually talk to something that speaks SMTP, and what bytes does that
something receive.
"""
import socket
import sys
import threading

HOST, PORT = "127.0.0.1", 2525


def session(conn, addr, out):
    f = conn.makefile("rwb")

    def say(line):
        out.append(f"S: {line}")
        f.write((line + "\r\n").encode())
        f.flush()

    say("220 sink.example.com ESMTP ready")
    in_data = False
    body = []
    while True:
        raw = f.readline()
        if not raw:
            break
        line = raw.decode("utf-8", "replace").rstrip("\r\n")
        if in_data:
            if line == ".":
                in_data = False
                say("250 2.0.0 Ok: queued as SINK-0001")
                continue
            body.append(line)
            continue
        out.append(f"C: {line}")
        up = line.upper()
        if up.startswith("EHLO") or up.startswith("HELO"):
            say("250-sink.example.com")
            say("250 SIZE 10485760")
        elif up.startswith("MAIL FROM") or up.startswith("RCPT TO"):
            say("250 2.1.0 Ok")
        elif up.startswith("DATA"):
            say("354 End data with <CR><LF>.<CR><LF>")
            in_data = True
        elif up.startswith("QUIT"):
            say("221 2.0.0 Bye")
            break
        else:
            say("250 2.0.0 Ok")
    conn.close()
    out.append("--- MESSAGE AS RECEIVED ---")
    out.extend(body)


def main():
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind((HOST, PORT))
    srv.listen(1)
    srv.settimeout(30)
    print(f"sink listening on {HOST}:{PORT}", flush=True)
    out = []
    try:
        conn, addr = srv.accept()
    except socket.timeout:
        print("no client connected within 30s", flush=True)
        sys.exit(2)
    session(conn, addr, out)
    print("\n".join(out), flush=True)
    srv.close()


if __name__ == "__main__":
    main()
