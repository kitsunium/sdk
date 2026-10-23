// echo_uring - an io_uring echo server: multishot accept, multishot recv into a
// provided-buffer ring, one send per received buffer. The kernel baseline the
// epoll one is paired with, and the shape the SDK's loop engine is built on.
//
// usage: echo_uring [-p port] [-d]
//   -d  IORING_SETUP_SINGLE_ISSUER | DEFER_TASKRUN instead of COOP_TASKRUN
//
// SIGUSR1 starts the measured window, SIGTERM or SIGINT stops and prints one
// result line. A "request" is one recv completion that carried bytes.
//
// user_data layout: op:8 | gen:8 | bid:16 | fd:32. gen is bumped on every
// accept so a completion that outlives its connection and meets a reused fd
// number is recognised and ignored.
#include <errno.h>
#include <liburing.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

#include "cref.h"

constexpr unsigned QUEUE_DEPTH = 4096;
constexpr unsigned NBUFS = 4096; // power of two
constexpr unsigned BUFSZ = 2048;
constexpr int BGID = 1;
constexpr size_t MAXFD = 1u << 20;

enum : unsigned { OP_ACCEPT = 1, OP_RECV = 2, OP_SEND = 3 };

static inline uint64_t ud_pack(unsigned op, unsigned gen, unsigned bid, int fd)
{
	return ((uint64_t)op << 56) | ((uint64_t)(gen & 0xffu) << 48) | ((uint64_t)(bid & 0xffffu) << 32) |
	       (uint32_t)fd;
}
static inline unsigned ud_op(uint64_t u) { return (unsigned)(u >> 56); }
static inline unsigned ud_gen(uint64_t u) { return (unsigned)((u >> 48) & 0xffu); }
static inline unsigned ud_bid(uint64_t u) { return (unsigned)((u >> 32) & 0xffffu); }
static inline int ud_fd(uint64_t u) { return (int)(uint32_t)u; }

static volatile sig_atomic_t g_stop;
static volatile sig_atomic_t g_reset;

static void on_term(int sig)
{
	(void)sig;
	g_stop = 1;
}

static void on_usr1(int sig)
{
	(void)sig;
	g_reset = 1;
}

static void die(const char *what, int err)
{
	fprintf(stderr, "%s: %s\n", what, strerror(err));
	exit(1);
}

static struct io_uring g_ring;
static struct io_uring_buf_ring *g_br;
static char *g_bufs;
static uint8_t *g_gen;     // by fd: generation of the live connection
static uint32_t *g_remain; // by bid: bytes of the buffer still to send
static uint32_t *g_sent;   // by bid: bytes of the buffer already sent
static int g_lfd;
static bool g_accept_armed;
static uint64_t g_requests, g_conns, g_errors, g_enobufs;

static struct io_uring_sqe *get_sqe(void)
{
	struct io_uring_sqe *sqe = io_uring_get_sqe(&g_ring);
	if (sqe == nullptr) {
		io_uring_submit(&g_ring);
		sqe = io_uring_get_sqe(&g_ring);
		if (sqe == nullptr)
			die("sq full", EBUSY);
	}
	return sqe;
}

static void arm_accept(void)
{
	struct io_uring_sqe *sqe = get_sqe();
	io_uring_prep_multishot_accept(sqe, g_lfd, nullptr, nullptr, SOCK_CLOEXEC);
	io_uring_sqe_set_data64(sqe, ud_pack(OP_ACCEPT, 0, 0, g_lfd));
	g_accept_armed = true;
}

static void arm_recv(int fd)
{
	struct io_uring_sqe *sqe = get_sqe();
	io_uring_prep_recv_multishot(sqe, fd, nullptr, 0, 0);
	sqe->flags |= IOSQE_BUFFER_SELECT;
	sqe->buf_group = BGID;
	io_uring_sqe_set_data64(sqe, ud_pack(OP_RECV, g_gen[fd], 0, fd));
}

static void arm_send(int fd, unsigned bid)
{
	struct io_uring_sqe *sqe = get_sqe();
	io_uring_prep_send(sqe, fd, g_bufs + (size_t)bid * BUFSZ + g_sent[bid], g_remain[bid], MSG_NOSIGNAL);
	io_uring_sqe_set_data64(sqe, ud_pack(OP_SEND, g_gen[fd], bid, fd));
}

static void recycle(unsigned bid)
{
	io_uring_buf_ring_add(g_br, g_bufs + (size_t)bid * BUFSZ, BUFSZ, (unsigned short)bid,
			      io_uring_buf_ring_mask(NBUFS), 0);
	io_uring_buf_ring_advance(g_br, 1);
}

static void handle_accept(const struct io_uring_cqe *cqe)
{
	if (!(cqe->flags & IORING_CQE_F_MORE))
		g_accept_armed = false;
	if (cqe->res >= 0) {
		int fd = cqe->res;
		if ((size_t)fd >= MAXFD) {
			close(fd);
			g_errors++;
		} else {
			g_gen[fd]++;
			g_conns++;
			arm_recv(fd);
		}
	} else {
		g_errors++;
	}
	// A dead accept is re-armed unless the process is out of fds; a close
	// re-arms it then.
	if (!g_accept_armed && cqe->res != -EMFILE && cqe->res != -ENFILE)
		arm_accept();
}

static void handle_recv(const struct io_uring_cqe *cqe)
{
	uint64_t ud = io_uring_cqe_get_data64(cqe);
	int fd = ud_fd(ud);
	bool more = cqe->flags & IORING_CQE_F_MORE;

	if (ud_gen(ud) != g_gen[fd]) {
		// A completion for a connection that is gone: return its buffer, nothing else.
		if (cqe->res > 0 && (cqe->flags & IORING_CQE_F_BUFFER))
			recycle(cqe->flags >> IORING_CQE_BUFFER_SHIFT);
		return;
	}
	if (cqe->res > 0 && (cqe->flags & IORING_CQE_F_BUFFER)) {
		unsigned bid = cqe->flags >> IORING_CQE_BUFFER_SHIFT;
		g_requests++;
		g_remain[bid] = (uint32_t)cqe->res;
		g_sent[bid] = 0;
		arm_send(fd, bid);
		if (!more)
			arm_recv(fd);
		return;
	}
	if (cqe->res == -ENOBUFS) {
		g_enobufs++;
		arm_recv(fd);
		return;
	}
	// res == 0 (peer closed) or a fatal error: the recv is finished, so closing
	// cannot race with it. Sends still in flight fail on the closed fd.
	if (more)
		return;
	close(fd);
	g_gen[fd]++;
	if (!g_accept_armed)
		arm_accept();
}

static void handle_send(const struct io_uring_cqe *cqe)
{
	uint64_t ud = io_uring_cqe_get_data64(cqe);
	unsigned bid = ud_bid(ud);
	int fd = ud_fd(ud);

	if (cqe->res > 0 && ud_gen(ud) == g_gen[fd] && (uint32_t)cqe->res < g_remain[bid]) {
		g_sent[bid] += (uint32_t)cqe->res; // short send: the rest goes out, same buffer
		g_remain[bid] -= (uint32_t)cqe->res;
		arm_send(fd, bid);
		return;
	}
	if (cqe->res < 0)
		g_errors++; // the peer's reset also reaches the recv, which closes
	recycle(bid);
}

int main(int argc, char **argv)
{
	int port = 9000;
	bool defer = false;

	for (int i = 1; i < argc; i++) {
		if (strcmp(argv[i], "-p") == 0 && i + 1 < argc)
			port = atoi(argv[++i]);
		else if (strcmp(argv[i], "-d") == 0)
			defer = true;
		else {
			fprintf(stderr, "unknown option %s\n", argv[i]);
			return 2;
		}
	}

	struct sigaction sa = {.sa_handler = on_term};
	sigaction(SIGTERM, &sa, nullptr);
	sigaction(SIGINT, &sa, nullptr);
	sa.sa_handler = on_usr1;
	sigaction(SIGUSR1, &sa, nullptr);
	signal(SIGPIPE, SIG_IGN);

	g_gen = calloc(MAXFD, sizeof *g_gen);
	g_remain = calloc(NBUFS, sizeof *g_remain);
	g_sent = calloc(NBUFS, sizeof *g_sent);
	g_bufs = aligned_alloc(4096, (size_t)NBUFS * BUFSZ);
	if (g_gen == nullptr || g_remain == nullptr || g_sent == nullptr || g_bufs == nullptr)
		die("alloc", ENOMEM);

	struct io_uring_params p = {0};
	p.flags = defer ? (IORING_SETUP_SINGLE_ISSUER | IORING_SETUP_DEFER_TASKRUN) : IORING_SETUP_COOP_TASKRUN;
	int rc = io_uring_queue_init_params(QUEUE_DEPTH, &g_ring, &p);
	if (rc < 0)
		die("io_uring_queue_init_params", -rc);

	int err = 0;
	g_br = io_uring_setup_buf_ring(&g_ring, NBUFS, BGID, 0, &err);
	if (g_br == nullptr)
		die("io_uring_setup_buf_ring", -err);
	for (unsigned i = 0; i < NBUFS; i++)
		io_uring_buf_ring_add(g_br, g_bufs + (size_t)i * BUFSZ, BUFSZ, (unsigned short)i,
				      io_uring_buf_ring_mask(NBUFS), (int)i);
	io_uring_buf_ring_advance(g_br, NBUFS);

	g_lfd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
	if (g_lfd < 0)
		die("socket", errno);
	int one = 1;
	setsockopt(g_lfd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof one);
	setsockopt(g_lfd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof one); // inherited by accepted sockets
	struct sockaddr_in addr = {.sin_family = AF_INET, .sin_port = htons((uint16_t)port)};
	if (bind(g_lfd, (struct sockaddr *)&addr, sizeof addr) != 0)
		die("bind", errno);
	if (listen(g_lfd, 4096) != 0)
		die("listen", errno);
	arm_accept();

	uint64_t base_requests = 0;
	struct cref_cpu base_cpu = cref_cpu_now();
	uint64_t base_ns = cref_now_ns();

	while (!g_stop) {
		if (g_reset) {
			g_reset = 0;
			base_requests = g_requests;
			base_cpu = cref_cpu_now();
			base_ns = cref_now_ns();
		}
		rc = io_uring_submit_and_wait(&g_ring, 1);
		if (rc < 0 && rc != -EINTR && rc != -EAGAIN && rc != -EBUSY)
			die("io_uring_submit_and_wait", -rc);

		struct io_uring_cqe *cqe;
		unsigned head, seen = 0;
		io_uring_for_each_cqe(&g_ring, head, cqe) {
			switch (ud_op(io_uring_cqe_get_data64(cqe))) {
			case OP_ACCEPT: handle_accept(cqe); break;
			case OP_RECV: handle_recv(cqe); break;
			case OP_SEND: handle_send(cqe); break;
			default: g_errors++; break;
			}
			seen++;
		}
		io_uring_cq_advance(&g_ring, seen);
	}

	struct cref_cpu now = cref_cpu_now();
	uint64_t reqs = g_requests - base_requests;
	double u = now.utime_us - base_cpu.utime_us;
	double s = now.stime_us - base_cpu.stime_us;
	double rq = reqs ? (double)reqs : 1.0;
	printf("bench=echo_uring requests=%llu conns_total=%llu errors=%llu enobufs=%llu window_s=%.3f utime_us=%.0f "
	       "stime_us=%.0f utime_us_per_req=%.5f stime_us_per_req=%.5f cpu_us_per_req=%.5f\n",
	       (unsigned long long)reqs, (unsigned long long)g_conns,
	       (unsigned long long)g_errors, (unsigned long long)g_enobufs,
	       (double)(cref_now_ns() - base_ns) / 1e9, u, s, u / rq, s / rq, (u + s) / rq);
	io_uring_queue_exit(&g_ring);
	return 0;
}
