// SPDX-License-Identifier: GPLv2

#ifndef __COMPAT_H_
#define __COMPAT_H_

#ifndef strnlen
size_t strnlen(const char *s, size_t maxlen) {
    size_t n;
    const char *e;

    for (e = s, n = 0; *e && n < maxlen; e++, n++)
        ;
    return n;
}
#endif

#ifndef strndup
char *strndup(const char *s1, size_t n) {
    char *s;

    n = strnlen(s1, n);

    if ((s = malloc(n + 1)) != NULL) {
        memcpy(s, s1, n);
        s[n] = '\0';
    }

    return s;
}
#endif

#endif