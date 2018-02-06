#ifndef __UTIL_H_
#define __UTIL_H_

#ifdef DEBUG
#define debug(M, ...) fprintf(stderr, "D(%s:%d) " M "\n", __FILE__, __LINE__, ##__VA_ARGS__)
#else
#define debug(M, ...)
#endif

#define clean_errno() (errno == 0 ? "None" : strerror(errno))
#define log_err(M, ...) fprintf(stderr, "E(%s:%d: errno: %s) " M "\n", __FILE__, __LINE__, clean_errno(), ##__VA_ARGS__)
#define log_warn(M, ...) \
    fprintf(stderr, "W(%s:%d: errno: %s) " M "\n", __FILE__, __LINE__, clean_errno(), ##__VA_ARGS__)
#define log_info(M, ...) fprintf(stderr, "I(%s:%d) " M "\n", __FILE__, __LINE__, ##__VA_ARGS__)
#define check(A, M, ...)           \
    if (!(A)) {                    \
        log_err(M, ##__VA_ARGS__); \
        errno = 0;                 \
        goto error;                \
    }

#define max(a, b)               \
    ({                          \
        __typeof__(a) _a = (a); \
        __typeof__(b) _b = (b); \
        _a > _b ? _a : _b;      \
    })

#define min(a, b)               \
    ({                          \
        __typeof__(a) _a = (a); \
        __typeof__(b) _b = (b); \
        _a < _b ? _a : _b;      \
    })

#define str_equal(s1, s2) (strcmp(s1, s2) == 0)                    // s2 is static string
#define str_starts_with(s1, s2) (strncmp(s1, s2, strlen(s2)) == 0) // s2 is static string
#define str_ends_with(s1, s1_len, s2) \
    (s1_len >= strlen(s2) && strncmp(s1 + s1_len - strlen(s2), s2, strlen(s2)) == 0) // s2 is static string

#endif /* __UTIL_H_ */
