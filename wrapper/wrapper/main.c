#include <errno.h>
#include <pthread.h>
#include <setjmp.h>
#include <signal.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#include <arpa/inet.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <sys/stat.h>

#include "cjson/cjson.h"
#include "cmdline.h"
#include "import.h"
#ifndef MyRelease
#include "subhook/subhook.c"
#include "subhook/subhook.h"
#endif

static struct shared_ptr apInf;
static uint8_t leaseMgr[16];
static struct shared_ptr reqCtx;
struct gengetopt_args_info args_info;
char *amUsername, *amPassword;
struct shared_ptr GUID;
int decryptCount = 1000;
int offlineFlag;
char *device_infos[9];

// Account info cache
static char *g_storefront_id = NULL;
static char *g_dev_token = NULL;
static char *g_music_token = NULL;

// Login status management
typedef enum {
  STATUS_NEED_LOGIN,
  STATUS_LOGGING_IN,
  STATUS_NEED_2FA,
  STATUS_LOGGED_IN,
  STATUS_LOGIN_FAILED
} login_status_t;

static login_status_t g_login_status = STATUS_NEED_LOGIN;
static pthread_mutex_t status_mutex = PTHREAD_MUTEX_INITIALIZER;
static char g_login_error[256] = {0};

// SSE client management
#define MAX_SSE_CLIENTS 16
static int sse_clients[MAX_SSE_CLIENTS];
static int sse_client_count = 0;
static pthread_mutex_t sse_mutex = PTHREAD_MUTEX_INITIALIZER;

// 2FA synchronization
static char g_2fa_code[8] = {0};
static int g_2fa_received = 0;
static pthread_cond_t g_2fa_cond = PTHREAD_COND_INITIALIZER;
static pthread_mutex_t g_2fa_mutex = PTHREAD_MUTEX_INITIALIZER;

// Login credentials (set by API)
static char g_username[256] = {0};
static char g_password[256] = {0};

// Thread support for concurrent connections
typedef struct {
  int connfd;
} thread_args_t;

// Mutex for thread-safe cache access
static pthread_mutex_t preshare_mutex = PTHREAD_MUTEX_INITIALIZER;

// Forward declarations for functions used in login_thread_func
int offline_available(void);
char *get_account_storefront_id(struct shared_ptr reqCtx);
char *get_dev_token(struct shared_ptr reqCtx);
char *get_music_user_token(char *guid, char *authToken,
                           struct shared_ptr reqCtx);
char *get_guid(void);
void write_storefront_id(void);
void write_music_token(void);
extern void *endLeaseCallback;
extern void *pbErrCallback;
static void *FHinstance;

#ifndef MyRelease
int32_t CURLOPT_SSL_VERIFYPEER = 64;
int32_t CURLOPT_SSL_VERIFYHOST = 81;
int32_t CURLOPT_PINNEDPUBLICKEY = 10230;

subhook_t curl_hook;

void curl_easy_setopt_hook(void *curl, int32_t option, ...) {
  va_list args;
  va_start(args, option);
  void *param = va_arg(args, void *);

  subhook_remove(curl_hook);

  if (option == CURLOPT_SSL_VERIFYPEER || option == CURLOPT_SSL_VERIFYHOST ||
      option == CURLOPT_PINNEDPUBLICKEY) {
    curl_easy_setopt(curl, option, 0L);
    printf("[+] hooked curl_easy_setopt %d\n", option);
  } else {
    curl_easy_setopt(curl, option, param);
  }

  va_end(args);
  subhook_install(curl_hook);
}

int android_log_print_hook(int prio, const char *tag, const char *fmt, ...) {
  char log_buffer[1024];
  va_list args;
  va_start(args, fmt);
  vsnprintf(log_buffer, sizeof(log_buffer), fmt, args);
  va_end(args);
  printf("[%s] %s\n", tag, log_buffer);
  return 0;
}

int android_log_write_hook(int prio, const char *tag, const char *text) {
  printf("[%s] %s\n", tag, text);
  return 0;
}

void DumpHex(const void *data, size_t size) {
  char ascii[17];
  size_t i, j;
  ascii[16] = '\0';
  for (i = 0; i < size; ++i) {
    printf("%02X ", ((unsigned char *)data)[i]);
    if (((unsigned char *)data)[i] >= ' ' &&
        ((unsigned char *)data)[i] <= '~') {
      ascii[i % 16] = ((unsigned char *)data)[i];
    } else {
      ascii[i % 16] = '.';
    }
    if ((i + 1) % 8 == 0 || i + 1 == size) {
      printf(" ");
      if ((i + 1) % 16 == 0) {
        printf("|  %s \n", ascii);
      } else if (i + 1 == size) {
        ascii[(i + 1) % 16] = '\0';
        if ((i + 1) % 16 <= 8) {
          printf(" ");
        }
        for (j = (i + 1) % 16; j < 16; ++j) {
          printf("   ");
        }
        printf("|  %s \n", ascii);
      }
    }
  }
}
#endif

int file_exists(char *filename) {
  struct stat buffer;
  return (stat(filename, &buffer) == 0);
}

char *strcat_b(char *dest, char *src) {
  size_t len1 = strlen(dest);
  size_t len2 = strlen(src);

  char *result = malloc(len1 + len2 + 1);
  if (!result)
    return NULL;

  strcpy(result, dest);
  strcat(result, src);

  return result;
}

int split_string_safe(const char *str, const char *delim, char **components,
                      int max_components, char **out_copy_to_free) {
  *out_copy_to_free = NULL;

  char *copy = strdup(str);
  if (copy == NULL) {
    return -1;
  }

  *out_copy_to_free = copy;

  int count = 0;
  char *saveptr;
  char *token;

  token = strtok_r(copy, delim, &saveptr);

  while (token != NULL && count < max_components) {
    components[count] = token;
    count++;
    token = strtok_r(NULL, delim, &saveptr);
  }

  return count;
}

static void dialogHandler(long j, struct shared_ptr *protoDialogPtr,
                          struct shared_ptr *respHandler) {
  const char *const title = std_string_data(
      _ZNK17storeservicescore14ProtocolDialog5titleEv(protoDialogPtr->obj));
  fprintf(stderr, "[.] dialogHandler: {title: %s, message: %s}\n", title,
          std_string_data(_ZNK17storeservicescore14ProtocolDialog7messageEv(
              protoDialogPtr->obj)));

  unsigned char ptr[72];
  memset(ptr + 8, 0, 16);
  *(void **)(ptr) =
      &_ZTVNSt6__ndk120__shared_ptr_emplaceIN17storeservicescore22ProtocolDialogResponseENS_9allocatorIS2_EEEE +
      2;
  struct shared_ptr diagResp = {.obj = ptr + 24, .ctrl_blk = ptr};
  _ZN17storeservicescore22ProtocolDialogResponseC1Ev(diagResp.obj);

  struct std_vector *butVec =
      _ZNK17storeservicescore14ProtocolDialog7buttonsEv(protoDialogPtr->obj);
  if (strcmp("Sign In", title) == 0) {
    for (struct shared_ptr *b = butVec->begin; b != butVec->end; ++b) {
      if (strcmp(
              "Use Existing Apple ID",
              std_string_data(_ZNK17storeservicescore14ProtocolButton5titleEv(
                  b->obj))) == 0) {
        _ZN17storeservicescore22ProtocolDialogResponse17setSelectedButtonERKNSt6__ndk110shared_ptrINS_14ProtocolButtonEEE(
            diagResp.obj, b);
        break;
      }
    }
  } else {
    for (struct shared_ptr *b = butVec->begin; b != butVec->end; ++b) {
      fprintf(stderr, "[.] button %p: %s\n", b->obj,
              std_string_data(
                  _ZNK17storeservicescore14ProtocolButton5titleEv(b->obj)));
    }
  }
  _ZN20androidstoreservices28AndroidPresentationInterface28handleProtocolDialogResponseERKlRKNSt6__ndk110shared_ptrIN17storeservicescore22ProtocolDialogResponseEEE(
      apInf.obj, &j, &diagResp);
}

// Forward declaration for set_login_status
static void set_login_status(login_status_t status);

static void credentialHandler(struct shared_ptr *credReqHandler,
                              struct shared_ptr *credRespHandler) {
  const uint8_t need2FA =
      _ZNK17storeservicescore18CredentialsRequest28requiresHSA2VerificationCodeEv(
          credReqHandler->obj);
  fprintf(stderr, "[.] credentialHandler: {title: %s, message: %s, 2FA: %s}\n",
          std_string_data(_ZNK17storeservicescore18CredentialsRequest5titleEv(
              credReqHandler->obj)),
          std_string_data(_ZNK17storeservicescore18CredentialsRequest7messageEv(
              credReqHandler->obj)),
          need2FA ? "true" : "false");

  int passLen = strlen(amPassword);

  if (need2FA) {
    if (args_info.code_from_file_flag) {
      fprintf(stderr, "[!] Enter your 2FA code into rootfs/%s/2fa.txt\n", args_info.base_dir_arg);
      fprintf(stderr, "[!] Example command: echo -n 114514 > rootfs/%s/2fa.txt\n", args_info.base_dir_arg);
      fprintf(stderr, "[!] Waiting for input...\n");
      int count = 0;
      while (1) {
        if (count >= 20) {
          fprintf(stderr, "[!] Failed to get 2FA Code in 60s. Exiting...\n");
          exit(0);
        }
        char *path = strcat_b(args_info.base_dir_arg, "/2fa.txt");
        if (file_exists(path)) {
          FILE *fp = fopen(path, "r");
          if (fp) {
            fscanf(fp, "%6s", amPassword + passLen);
            fclose(fp);
            remove(path);
            fprintf(stderr, "[!] Code file detected! Logging in...\n");
            break;
          }
        }
        sleep(3);
        count++;
      }
    } else {
      // API-driven 2FA: signal status and wait for /2fa endpoint
      set_login_status(STATUS_NEED_2FA);
      fprintf(stderr, "[!] Waiting for 2FA code via API...\n");

      // Wait for 2FA code with 60 second timeout
      struct timespec timeout;
      clock_gettime(CLOCK_REALTIME, &timeout);
      timeout.tv_sec += 60;

      pthread_mutex_lock(&g_2fa_mutex);
      while (!g_2fa_received) {
        int ret = pthread_cond_timedwait(&g_2fa_cond, &g_2fa_mutex, &timeout);
        if (ret == ETIMEDOUT) {
          pthread_mutex_unlock(&g_2fa_mutex);
          fprintf(stderr, "[!] 2FA timeout after 60s\n");
          snprintf(g_login_error, sizeof(g_login_error), "2FA timeout");
          set_login_status(STATUS_LOGIN_FAILED);
          return;
        }
      }
      // Copy 2FA code to password
      strncpy(amPassword + passLen, g_2fa_code, 6);
      amPassword[passLen + 6] = '\0';
      g_2fa_received = 0;
      pthread_mutex_unlock(&g_2fa_mutex);
      fprintf(stderr, "[!] 2FA code received via API, continuing login...\n");
    }
  }

  uint8_t *const ptr = malloc(80);
  memset(ptr + 8, 0, 16);
  *(void **)(ptr) =
      &_ZTVNSt6__ndk120__shared_ptr_emplaceIN17storeservicescore19CredentialsResponseENS_9allocatorIS2_EEEE +
      2;
  struct shared_ptr credResp = {.obj = ptr + 24, .ctrl_blk = ptr};
  _ZN17storeservicescore19CredentialsResponseC1Ev(credResp.obj);

  union std_string username = new_std_string(amUsername);
  _ZN17storeservicescore19CredentialsResponse11setUserNameERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEE(
      credResp.obj, &username);

  union std_string password = new_std_string(amPassword);
  _ZN17storeservicescore19CredentialsResponse11setPasswordERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEE(
      credResp.obj, &password);

  _ZN17storeservicescore19CredentialsResponse15setResponseTypeENS0_12ResponseTypeE(
      credResp.obj, 2);

  _ZN20androidstoreservices28AndroidPresentationInterface25handleCredentialsResponseERKNSt6__ndk110shared_ptrIN17storeservicescore19CredentialsResponseEEE(
      apInf.obj, &credResp);
}

#ifndef MyRelease
static uint8_t allDebug() { return 1; }
#endif

static inline void init() {
  // srand(time(0));

  // raise(SIGSTOP);
  fprintf(stderr, "[+] starting...\n");

  // Ignore SIGPIPE to prevent process termination when writing to closed
  // sockets
  signal(SIGPIPE, SIG_IGN);

  setenv("ANDROID_DNS_MODE", "local", 1);
  if (args_info.proxy_given) {
    fprintf(stderr, "[+] Using proxy %s\n", args_info.proxy_arg);
    setenv("all_proxy", args_info.proxy_arg, 1);
  }

  static const char *resolvers[2] = {"223.5.5.5", "223.6.6.6"};
  _resolv_set_nameservers_for_net(0, resolvers, 2, ".");

  // static char android_id[16];
  // for (int i = 0; i < 16; ++i) {
  //     android_id[i] = "0123456789abcdef"[rand() % 16];
  // }
  union std_string conf1 = new_std_string(device_infos[8]);
  union std_string conf2 = new_std_string("");
  _ZN14FootHillConfig6configERKNSt6__ndk112basic_stringIcNS0_11char_traitsIcEENS0_9allocatorIcEEEE(
      &conf1);

  // union std_string root = new_std_string("/");
  // union std_string natLib = new_std_string("/system/lib64/");
  // void *foothill = malloc(120);
  // _ZN8FootHillC2ERKNSt6__ndk112basic_stringIcNS0_11char_traitsIcEENS0_9allocatorIcEEEES8_(
  //     foothill, &root, &natLib);
  // _ZN8FootHill24defaultContextIdentifierEv(foothill);

  _ZN17storeservicescore10DeviceGUID8instanceEv(&GUID);

  static uint8_t ret[88];
  static unsigned int conf3 = 29;
  static uint8_t conf4 = 1;
  _ZN17storeservicescore10DeviceGUID9configureERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEES9_RKjRKb(
      &ret, GUID.obj, &conf1, &conf2, &conf3, &conf4);
}

static inline struct shared_ptr init_ctx() {
  fprintf(stderr, "[+] initializing ctx...\n");
  union std_string strBuf =
      new_std_string(strcat_b(args_info.base_dir_arg, "/mpl_db"));

  struct shared_ptr reqCtx;
  _ZNSt6__ndk110shared_ptrIN17storeservicescore14RequestContextEE11make_sharedIJRNS_12basic_stringIcNS_11char_traitsIcEENS_9allocatorIcEEEEEEES3_DpOT_(
      &reqCtx, &strBuf);

  static uint8_t ptr[480];
  *(void **)(ptr) =
      &_ZTVNSt6__ndk120__shared_ptr_emplaceIN17storeservicescore20RequestContextConfigENS_9allocatorIS2_EEEE +
      2;
  struct shared_ptr reqCtxCfg = {.obj = ptr + 32, .ctrl_blk = ptr};

  _ZN17storeservicescore20RequestContextConfigC2Ev(reqCtxCfg.obj);
  // _ZN17storeservicescore20RequestContextConfig9setCPFlagEb(reqCtx.obj, 1);
  _ZN17storeservicescore20RequestContextConfig20setBaseDirectoryPathERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEE(
      reqCtxCfg.obj, &strBuf);
  strBuf = new_std_string(device_infos[0]);
  _ZN17storeservicescore20RequestContextConfig19setClientIdentifierERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEE(
      reqCtxCfg.obj, &strBuf);
  strBuf = new_std_string(device_infos[1]);
  _ZN17storeservicescore20RequestContextConfig20setVersionIdentifierERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEE(
      reqCtxCfg.obj, &strBuf);
  strBuf = new_std_string(device_infos[2]);
  _ZN17storeservicescore20RequestContextConfig21setPlatformIdentifierERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEE(
      reqCtxCfg.obj, &strBuf);
  strBuf = new_std_string(device_infos[3]);
  _ZN17storeservicescore20RequestContextConfig17setProductVersionERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEE(
      reqCtxCfg.obj, &strBuf);
  strBuf = new_std_string(device_infos[4]);
  _ZN17storeservicescore20RequestContextConfig14setDeviceModelERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEE(
      reqCtxCfg.obj, &strBuf);
  strBuf = new_std_string(device_infos[5]);
  _ZN17storeservicescore20RequestContextConfig15setBuildVersionERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEE(
      reqCtxCfg.obj, &strBuf);
  strBuf = new_std_string(device_infos[6]);
  _ZN17storeservicescore20RequestContextConfig19setLocaleIdentifierERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEE(
      reqCtxCfg.obj, &strBuf);
  strBuf = new_std_string(device_infos[7]);
  _ZN17storeservicescore20RequestContextConfig21setLanguageIdentifierERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEE(
      reqCtxCfg.obj, &strBuf);

  _ZN21RequestContextManager9configureERKNSt6__ndk110shared_ptrIN17storeservicescore14RequestContextEEE(
      &reqCtx);
  static uint8_t buf[88];
  _ZN17storeservicescore14RequestContext4initERKNSt6__ndk110shared_ptrINS_20RequestContextConfigEEE(
      &buf, reqCtx.obj, &reqCtxCfg);
  strBuf = new_std_string(args_info.base_dir_arg);
  _ZN17storeservicescore14RequestContext24setFairPlayDirectoryPathERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEE(
      reqCtx.obj, &strBuf);

  _ZNSt6__ndk110shared_ptrIN20androidstoreservices28AndroidPresentationInterfaceEE11make_sharedIJEEES3_DpOT_(
      &apInf);

  _ZN20androidstoreservices28AndroidPresentationInterface16setDialogHandlerEPFvlNSt6__ndk110shared_ptrIN17storeservicescore14ProtocolDialogEEENS2_INS_36AndroidProtocolDialogResponseHandlerEEEE(
      apInf.obj, &dialogHandler);

  _ZN20androidstoreservices28AndroidPresentationInterface21setCredentialsHandlerEPFvNSt6__ndk110shared_ptrIN17storeservicescore18CredentialsRequestEEENS2_INS_33AndroidCredentialsResponseHandlerEEEE(
      apInf.obj, &credentialHandler);

  _ZN17storeservicescore14RequestContext24setPresentationInterfaceERKNSt6__ndk110shared_ptrINS_21PresentationInterfaceEEE(
      reqCtx.obj, &apInf);

  return reqCtx;
}

static inline uint8_t readfull(const int connfd, void *const buf,
                               const size_t size) {
  size_t red = 0;
  while (size > red) {
    const ssize_t b = read(connfd, ((uint8_t *)buf) + red, size - red);
    if (b <= 0)
      return 0;
    red += b;
  }
  return 1;
}

static inline void writefull(const int connfd, void *const buf,
                             const size_t size) {
  size_t red = 0;
  while (size > red) {
    const ssize_t b = write(connfd, ((uint8_t *)buf) + red, size - red);
    if (b <= 0) {
      perror("write");
      break;
    }
    red += b;
  }
}

static void *preshareCtx = NULL;

inline static void *getKdContext(const char *const adam,
                                 const char *const uri) {
  uint8_t isPreshare = (strcmp("0", adam) == 0);

  // Thread-safe check for cached preshare context
  if (isPreshare) {
    pthread_mutex_lock(&preshare_mutex);
    if (preshareCtx != NULL) {
      void *ctx = preshareCtx;
      pthread_mutex_unlock(&preshare_mutex);
      return ctx;
    }
    pthread_mutex_unlock(&preshare_mutex);
  }

  fprintf(stderr, "[.] adamId: %s, uri: %s\n", adam, uri);

  union std_string defaultId = new_std_string(adam);
  union std_string keyUri = new_std_string(uri);
  union std_string keyFormat = new_std_string("com.apple.streamingkeydelivery");
  union std_string keyFormatVer = new_std_string("1");
  union std_string serverUri = new_std_string(
      "https://play.itunes.apple.com/WebObjects/MZPlay.woa/music/fps");
  union std_string protocolType = new_std_string("simplified");
  union std_string fpsCert = new_std_string(fairplayCert);

  struct shared_ptr persistK = {.obj = NULL};

  // FairPlay library calls - running without lock for maximum parallelism
  _ZN21SVFootHillSessionCtrl16getPersistentKeyERKNSt6__ndk112basic_stringIcNS0_11char_traitsIcEENS0_9allocatorIcEEEES8_S8_S8_S8_S8_S8_S8_(
      &persistK, FHinstance, &defaultId, &defaultId, &keyUri, &keyFormat,
      &keyFormatVer, &serverUri, &protocolType, &fpsCert);

  if (persistK.obj == NULL) {
    return NULL;
  }

  struct shared_ptr SVFootHillPContext;
  _ZN21SVFootHillSessionCtrl14decryptContextERKNSt6__ndk112basic_stringIcNS0_11char_traitsIcEENS0_9allocatorIcEEEERKN11SVDecryptor15SVDecryptorTypeERKb(
      &SVFootHillPContext, FHinstance, persistK.obj);

  if (SVFootHillPContext.obj == NULL)
    return NULL;

  void *kdContext =
      *_ZNK18SVFootHillPContext9kdContextEv(SVFootHillPContext.obj);

  // Thread-safe cache of preshare context
  if (kdContext != NULL && isPreshare) {
    pthread_mutex_lock(&preshare_mutex);
    preshareCtx = kdContext;
    pthread_mutex_unlock(&preshare_mutex);
  }
  return kdContext;
}

void refresh_decrypt_ctx() {
  uint8_t autom = 1;
  _ZN22SVPlaybackLeaseManager12requestLeaseERKb(leaseMgr, &autom);
  _ZN21SVFootHillSessionCtrl16resetAllContextsEv(FHinstance);
  preshareCtx = NULL;
  preshareCtx = getKdContext("0", "skd://itunes.apple.com/P000000000/s1/e1");
  printf("[!] refreshed context\n");
}

void handle(const int connfd) {
  while (1) {
    uint8_t adamSize;
    if (!readfull(connfd, &adamSize, sizeof(uint8_t)))
      return;
    if (adamSize <= 0)
      return;

    char adam[adamSize + 1];
    if (!readfull(connfd, adam, adamSize))
      return;
    adam[adamSize] = '\0';

    uint8_t uri_size;
    if (!readfull(connfd, &uri_size, sizeof(uint8_t)))
      return;

    char uri[uri_size + 1];
    if (!readfull(connfd, uri, uri_size))
      return;
    uri[uri_size] = '\0';

    void **const kdContext = getKdContext(adam, uri);
    if (kdContext == NULL)
      return;

    while (1) {
      uint32_t size;
      if (!readfull(connfd, &size, sizeof(uint32_t))) {
        perror("read");
        return;
      }

      if (size <= 0)
        break;

      void *sample = malloc(size);
      if (sample == NULL) {
        perror("malloc");
        return;
      }
      if (!readfull(connfd, sample, size)) {
        free(sample);
        perror("read");
        return;
      }

      NfcRKVnxuKZy04KWbdFu71Ou(*kdContext, 5, sample, sample, size);
      writefull(connfd, sample, size);
      free(sample);
    }
  }
}

extern uint8_t handle_cpp(int);

// Worker thread for handling decrypt connections
static void *decrypt_worker_thread(void *arg) {
  thread_args_t *args = (thread_args_t *)arg;
  int connfd = args->connfd;
  free(args); // Free the argument struct immediately

  fprintf(stderr, "[+] decrypt worker thread started for fd %d\n", connfd);

  if (!handle_cpp(connfd)) {
    uint8_t autom = 1;
    _ZN22SVPlaybackLeaseManager12requestLeaseERKb(leaseMgr, &autom);
  }

  if (close(connfd) == -1) {
    perror("close");
  }

  fprintf(stderr, "[+] decrypt worker thread finished for fd %d\n", connfd);
  return NULL;
}

inline static int new_socket() {
  const int fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, IPPROTO_TCP);
  if (fd == -1) {
    perror("socket");
    return EXIT_FAILURE;
  }
  const int optval = 1;
  setsockopt(fd, SOL_SOCKET, SO_REUSEPORT, &optval, sizeof(optval));

  static struct sockaddr_in serv_addr = {.sin_family = AF_INET};
  inet_pton(AF_INET, args_info.host_arg, &serv_addr.sin_addr);
  serv_addr.sin_port = htons(args_info.decrypt_port_arg);
  if (bind(fd, (struct sockaddr *)&serv_addr, sizeof(serv_addr)) == -1) {
    perror("bind");
    return EXIT_FAILURE;
  }

  if (listen(fd, 32) == -1) { // Increased backlog for concurrent connections
    perror("listen");
    return EXIT_FAILURE;
  }

  fprintf(stderr, "[!] listening %s:%d (concurrent mode)\n", args_info.host_arg,
          args_info.decrypt_port_arg);

  static struct sockaddr_in peer_addr;
  static socklen_t peer_addr_size = sizeof(peer_addr);
  while (1) {
    const int connfd = accept4(fd, (struct sockaddr *)&peer_addr,
                               &peer_addr_size, SOCK_CLOEXEC);
    if (connfd == -1) {
      if (errno == ENETDOWN || errno == EPROTO || errno == ENOPROTOOPT ||
          errno == EHOSTDOWN || errno == ENONET || errno == EHOSTUNREACH ||
          errno == EOPNOTSUPP || errno == ENETUNREACH)
        continue;
      perror("accept4");
      return EXIT_FAILURE;
    }

    // Create thread arguments
    thread_args_t *args = malloc(sizeof(thread_args_t));
    if (args == NULL) {
      perror("malloc thread args");
      close(connfd);
      continue;
    }
    args->connfd = connfd;

    // Create worker thread for this connection
    pthread_t worker_thread;
    if (pthread_create(&worker_thread, NULL, decrypt_worker_thread, args) !=
        0) {
      perror("pthread_create");
      free(args);
      close(connfd);
      continue;
    }
    pthread_detach(worker_thread); // Auto-cleanup when thread finishes
  }
}

const char *get_m3u8_method_download(struct shared_ptr reqCtx,
                                     unsigned long adam) {
  void *purchase_request = malloc(1024);
  _ZN17storeservicescore15PurchaseRequestC2ERKNSt6__ndk110shared_ptrINS_14RequestContextEEE(
      purchase_request, &reqCtx);
  _ZN17storeservicescore15PurchaseRequest23setProcessDialogActionsEb(
      purchase_request, 1);
  union std_string urlBagKey = new_std_string("subDownload");
  _ZN17storeservicescore15PurchaseRequest12setURLBagKeyERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEE(
      purchase_request, &urlBagKey);
  char *buyParametersStr = malloc(128);
  sprintf(buyParametersStr,
          "salableAdamId=%lu&price=0&pricingParameters=SUBS&productType=S",
          adam);
  union std_string buyParameters = new_std_string(buyParametersStr);
  _ZN17storeservicescore15PurchaseRequest16setBuyParametersERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEE(
      purchase_request, &buyParameters);
  _ZN17storeservicescore15PurchaseRequest3runEv(purchase_request);
  struct shared_ptr *response =
      _ZNK17storeservicescore15PurchaseRequest8responseEv(purchase_request);
  struct shared_ptr *error =
      _ZN17storeservicescore16PurchaseResponse5errorEv(response->obj);
  ;
  if (error->obj == NULL) {
    struct std_vector items =
        _ZNK17storeservicescore16PurchaseResponse5itemsEv(response->obj);
    struct shared_ptr *firstItem = items.begin;
    struct std_vector assets =
        _ZNK17storeservicescore12PurchaseItem6assetsEv(firstItem->obj);
    struct shared_ptr *lastAsset = (struct shared_ptr *)assets.end - 1;
    union std_string *url_str = malloc(sizeof(union std_string));
    _ZNK17storeservicescore13PurchaseAsset3URLEv(url_str, lastAsset->obj);
    const char *url = std_string_data(url_str);
    if (url) {
      char *result = strdup(url); // Make a copy
      free(url_str);
      return result;
    }
  }
  return NULL;
}

const char *get_m3u8_method_play(uint8_t leaseMgr[16], unsigned long adam) {
  union std_string HLS = new_std_string_short_mode("HLS");
  struct std_vector HLSParam = new_std_vector(&HLS);
  static uint8_t z0 = 0;
  struct shared_ptr ptr_result;
  _ZN22SVPlaybackLeaseManager12requestAssetERKmRKNSt6__ndk16vectorINS2_12basic_stringIcNS2_11char_traitsIcEENS2_9allocatorIcEEEENS7_IS9_EEEERKb(
      &ptr_result, leaseMgr, &adam, &HLSParam, &z0);

  if (ptr_result.obj == NULL) {
    return NULL;
  }

  if (_ZNK23SVPlaybackAssetResponse13hasValidAssetEv(ptr_result.obj)) {
    struct shared_ptr *playbackAsset =
        _ZNK23SVPlaybackAssetResponse13playbackAssetEv(ptr_result.obj);
    if (playbackAsset == NULL || playbackAsset->obj == NULL) {
      return NULL;
    }

    union std_string *m3u8 = malloc(sizeof(union std_string));
    if (m3u8 == NULL) {
      return NULL;
    }

    void *playbackObj = playbackAsset->obj;
    _ZNK17storeservicescore13PlaybackAsset9URLStringEv(m3u8, playbackObj);

    if (m3u8 == NULL || std_string_data(m3u8) == NULL) {
      free(m3u8);
      return NULL;
    }

    const char *m3u8_str = std_string_data(m3u8);
    if (m3u8_str) {
      char *result = strdup(m3u8_str); // Make a copy
      free(m3u8);
      return result;
    } else {
      return NULL;
    }
  } else {
    return NULL;
  }
}

void handle_m3u8(const int connfd) {
  while (1) {
    uint8_t adamSize;
    if (!readfull(connfd, &adamSize, sizeof(uint8_t))) {
      return;
    }
    if (adamSize <= 0) {
      return;
    }
    char adam[adamSize];
    for (int i = 0; i < adamSize; i = i + 1) {
      readfull(connfd, &adam[i], sizeof(uint8_t));
    }
    char *ptr;
    unsigned long adamID = strtoul(adam, &ptr, 10);
    const char *m3u8;
    if (offlineFlag) {
      m3u8 = get_m3u8_method_download(reqCtx, adamID);
    } else {
      m3u8 = get_m3u8_method_play(leaseMgr, adamID);
    }
    if (m3u8 == NULL) {
      fprintf(stderr, "[.] failed to get m3u8 of adamId: %ld\n", adamID);
      writefull(connfd, "\n", sizeof("\n"));
    } else {
      fprintf(stderr, "[.] m3u8 adamId: %ld, url: %s\n", adamID, m3u8);
      char *with_newline = malloc(strlen(m3u8) + 2);
      if (with_newline) {
        strcpy(with_newline, m3u8);
        strcat(with_newline, "\n");
        writefull(connfd, with_newline, strlen(with_newline));
        free(with_newline);
      }
      free((void *)m3u8);
    }
  }
}

// Worker thread for handling m3u8 connections
static void *m3u8_worker_thread(void *arg) {
  thread_args_t *args = (thread_args_t *)arg;
  int connfd = args->connfd;
  free(args);

  handle_m3u8(connfd);

  if (close(connfd) == -1) {
    perror("close");
  }

  return NULL;
}

static inline void *new_socket_m3u8(void *args) {
  const int fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, IPPROTO_TCP);
  if (fd == -1) {
    perror("socket");
  }
  const int optval = 1;
  setsockopt(fd, SOL_SOCKET, SO_REUSEPORT, &optval, sizeof(optval));

  static struct sockaddr_in serv_addr = {.sin_family = AF_INET};
  inet_pton(AF_INET, args_info.host_arg, &serv_addr.sin_addr);
  serv_addr.sin_port = htons(args_info.m3u8_port_arg);
  if (bind(fd, (struct sockaddr *)&serv_addr, sizeof(serv_addr)) == -1) {
    perror("bind");
  }

  if (listen(fd, 32) == -1) { // Increased backlog for concurrent connections
    perror("listen");
  }

  // removed print statement from here to prevent premature ready status signal

  static struct sockaddr_in peer_addr;
  static socklen_t peer_addr_size = sizeof(peer_addr);
  while (1) {
    const int connfd = accept4(fd, (struct sockaddr *)&peer_addr,
                               &peer_addr_size, SOCK_CLOEXEC);
    if (connfd == -1) {
      if (errno == ENETDOWN || errno == EPROTO || errno == ENOPROTOOPT ||
          errno == EHOSTDOWN || errno == ENONET || errno == EHOSTUNREACH ||
          errno == EOPNOTSUPP || errno == ENETUNREACH)
        continue;
      perror("accept4");
    }

    // Create thread arguments
    thread_args_t *thread_args = malloc(sizeof(thread_args_t));
    if (thread_args == NULL) {
      perror("malloc thread args");
      close(connfd);
      continue;
    }
    thread_args->connfd = connfd;

    // Create worker thread for this connection
    pthread_t worker_thread;
    if (pthread_create(&worker_thread, NULL, m3u8_worker_thread, thread_args) !=
        0) {
      perror("pthread_create");
      free(thread_args);
      close(connfd);
      continue;
    }
    pthread_detach(worker_thread);
  }
}

// ===== Account API Helper Functions =====

// Forward declaration
static void remove_sse_client(int connfd);

static const char *get_status_string(login_status_t status) {
  switch (status) {
  case STATUS_NEED_LOGIN:
    return "need_login";
  case STATUS_LOGGING_IN:
    return "logging_in";
  case STATUS_NEED_2FA:
    return "need_2fa";
  case STATUS_LOGGED_IN:
    return "logged_in";
  case STATUS_LOGIN_FAILED:
    return "login_failed";
  default:
    return "unknown";
  }
}

static int send_sse_event(int connfd, const char *event_data) {
  char buffer[512];
  snprintf(buffer, sizeof(buffer), "data: %s\n\n", event_data);
  ssize_t written = write(connfd, buffer, strlen(buffer));
  if (written < 0) {
    return -1; // Client disconnected
  }
  return 0;
}

static void broadcast_sse_status(login_status_t status) {
  char event_data[512];
  char timestamp[32];
  time_t now = time(NULL);
  struct tm *tm_info = localtime(&now);
  strftime(timestamp, sizeof(timestamp), "%Y-%m-%dT%H:%M:%S", tm_info);

  if (status == STATUS_LOGIN_FAILED && g_login_error[0] != '\0') {
    snprintf(event_data, sizeof(event_data),
             "{\"type\":\"status_change\",\"status\":\"%s\",\"error\":\"%s\","
             "\"timestamp\":\"%s\"}",
             get_status_string(status), g_login_error, timestamp);
  } else {
    snprintf(
        event_data, sizeof(event_data),
        "{\"type\":\"status_change\",\"status\":\"%s\",\"timestamp\":\"%s\"}",
        get_status_string(status), timestamp);
  }

  pthread_mutex_lock(&sse_mutex);
  int disconnected[MAX_SSE_CLIENTS];
  int disconnect_count = 0;

  for (int i = 0; i < sse_client_count; i++) {
    if (send_sse_event(sse_clients[i], event_data) < 0) {
      disconnected[disconnect_count++] = sse_clients[i];
    }
  }
  pthread_mutex_unlock(&sse_mutex);

  // Remove disconnected clients
  for (int i = 0; i < disconnect_count; i++) {
    remove_sse_client(disconnected[i]);
    fprintf(stderr, "[SSE] client fd=%d disconnected during broadcast\n",
            disconnected[i]);
  }

  fprintf(stderr, "[SSE] broadcast: %s (clients=%d)\n", event_data,
          sse_client_count);
}

static void set_login_status(login_status_t status) {
  pthread_mutex_lock(&status_mutex);
  login_status_t old_status = g_login_status;
  g_login_status = status;
  pthread_mutex_unlock(&status_mutex);

  fprintf(stderr, "[.] login status changed: %s -> %s\n",
          get_status_string(old_status), get_status_string(status));

  broadcast_sse_status(status);
}

static void remove_sse_client(int connfd) {
  pthread_mutex_lock(&sse_mutex);
  for (int i = 0; i < sse_client_count; i++) {
    if (sse_clients[i] == connfd) {
      // Shift remaining clients
      for (int j = i; j < sse_client_count - 1; j++) {
        sse_clients[j] = sse_clients[j + 1];
      }
      sse_client_count--;
      break;
    }
  }
  pthread_mutex_unlock(&sse_mutex);
}

static void send_json_response(int connfd, int status_code,
                               const char *json_body) {
  const char *status_text;
  switch (status_code) {
  case 200:
    status_text = "OK";
    break;
  case 202:
    status_text = "Accepted";
    break;
  case 400:
    status_text = "Bad Request";
    break;
  case 404:
    status_text = "Not Found";
    break;
  case 500:
    status_text = "Internal Server Error";
    break;
  default:
    status_text = "Unknown";
    break;
  }

  int body_len = strlen(json_body);
  char headers[512];
  snprintf(headers, sizeof(headers),
           "HTTP/1.1 %d %s\r\n"
           "Content-Type: application/json\r\n"
           "Content-Length: %d\r\n"
           "Connection: close\r\n\r\n",
           status_code, status_text, body_len);

  writefull(connfd, headers, strlen(headers));
  writefull(connfd, (void *)json_body, body_len);
}

static void parse_http_request(const char *buffer, char *method, char *path) {
  sscanf(buffer, "%15s %255s", method, path);
}

static const char *find_json_body(const char *request) {
  const char *body = strstr(request, "\r\n\r\n");
  if (body)
    return body + 4;
  return NULL;
}

// ===== Login Thread Function =====

static void *login_thread_func(void *arg) {
  set_login_status(STATUS_LOGGING_IN);

  // Set credentials for credentialHandler
  amUsername = g_username;
  amPassword = g_password;

  fprintf(stderr, "[+] logging in via API...\n");

  // Delete old tokens
  if (file_exists(strcat_b(args_info.base_dir_arg, "/STOREFRONT_ID"))) {
    remove(strcat_b(args_info.base_dir_arg, "/STOREFRONT_ID"));
  }
  if (file_exists(strcat_b(args_info.base_dir_arg, "/MUSIC_TOKEN"))) {
    remove(strcat_b(args_info.base_dir_arg, "/MUSIC_TOKEN"));
  }

  struct shared_ptr flow;
  _ZNSt6__ndk110shared_ptrIN17storeservicescore16AuthenticateFlowEE11make_sharedIJRNS0_INS1_14RequestContextEEEEEES3_DpOT_(
      &flow, &reqCtx);
  _ZN17storeservicescore16AuthenticateFlow3runEv(flow.obj);
  struct shared_ptr *resp =
      _ZNK17storeservicescore16AuthenticateFlow8responseEv(flow.obj);

  if (resp == NULL || resp->obj == NULL) {
    snprintf(g_login_error, sizeof(g_login_error), "authentication failed");
    set_login_status(STATUS_LOGIN_FAILED);
    return NULL;
  }

  const int respType =
      _ZNK17storeservicescore20AuthenticateResponse12responseTypeEv(resp->obj);
  fprintf(stderr, "[.] login response type: %d\n", respType);

  if (respType != 6) {
    snprintf(g_login_error, sizeof(g_login_error), "login failed with code %d",
             respType);
    set_login_status(STATUS_LOGIN_FAILED);
    return NULL;
  }

  // Login successful, initialize services
  _ZN22SVPlaybackLeaseManagerC2ERKNSt6__ndk18functionIFvRKiEEERKNS1_IFvRKNS0_10shared_ptrIN17storeservicescore19StoreErrorConditionEEEEEE(
      leaseMgr, &endLeaseCallback, &pbErrCallback);
  uint8_t autom = 1;
  _ZN22SVPlaybackLeaseManager25refreshLeaseAutomaticallyERKb(leaseMgr, &autom);
  _ZN22SVPlaybackLeaseManager12requestLeaseERKb(leaseMgr, &autom);
  FHinstance = _ZN21SVFootHillSessionCtrl8instanceEv();

  offlineFlag = offline_available();
  if (offlineFlag) {
    printf("[+] This account supports offline channel\n");
  }

  // Cache account info
  g_storefront_id = get_account_storefront_id(reqCtx);
  g_dev_token = get_dev_token(reqCtx);
  g_music_token = get_music_user_token(get_guid(), g_dev_token, reqCtx);
  fprintf(stderr, "[+] account info cached successfully\n");

  write_storefront_id();
  write_music_token();
  fprintf(stderr, "[!] listening m3u8 request on %s:%d (concurrent mode)\n",
          args_info.host_arg, args_info.m3u8_port_arg);

  set_login_status(STATUS_LOGGED_IN);
  return NULL;
}

// ===== API Endpoint Handlers =====

static void handle_info(int connfd) {
  pthread_mutex_lock(&status_mutex);
  login_status_t status = g_login_status;
  pthread_mutex_unlock(&status_mutex);

  char json_body[1024];
  if (status == STATUS_LOGGED_IN) {
    snprintf(json_body, sizeof(json_body),
             "{\"logged_in\":true,\"storefront_id\":\"%s\","
             "\"dev_token\":\"%s\",\"music_token\":\"%s\"}",
             g_storefront_id ? g_storefront_id : "",
             g_dev_token ? g_dev_token : "",
             g_music_token ? g_music_token : "");
  } else {
    snprintf(json_body, sizeof(json_body),
             "{\"logged_in\":false,\"status\":\"%s\"}",
             get_status_string(status));
  }

  fprintf(stderr, "[.] /info: status=%s\n", get_status_string(status));
  send_json_response(connfd, 200, json_body);
}

static void handle_login(int connfd, const char *request) {
  pthread_mutex_lock(&status_mutex);
  login_status_t status = g_login_status;
  pthread_mutex_unlock(&status_mutex);

  if (status == STATUS_LOGGED_IN) {
    send_json_response(connfd, 400, "{\"error\":\"already logged in\"}");
    return;
  }
  if (status == STATUS_LOGGING_IN || status == STATUS_NEED_2FA) {
    send_json_response(connfd, 400, "{\"error\":\"login in progress\"}");
    return;
  }

  // Parse JSON body
  const char *body = find_json_body(request);
  if (!body) {
    send_json_response(connfd, 400, "{\"error\":\"missing request body\"}");
    return;
  }

  cJSON *json = cJSON_Parse(body);
  if (!json) {
    send_json_response(connfd, 400, "{\"error\":\"invalid JSON\"}");
    return;
  }

  cJSON *username_obj = cJSON_GetObjectItemCaseSensitive(json, "username");
  cJSON *password_obj = cJSON_GetObjectItemCaseSensitive(json, "password");

  if (!cJSON_IsString(username_obj) || !cJSON_IsString(password_obj)) {
    cJSON_Delete(json);
    send_json_response(connfd, 400,
                       "{\"error\":\"username and password required\"}");
    return;
  }

  // Store credentials
  strncpy(g_username, username_obj->valuestring, sizeof(g_username) - 1);
  strncpy(g_password, password_obj->valuestring, sizeof(g_password) - 1);
  cJSON_Delete(json);

  // Reset 2FA state
  pthread_mutex_lock(&g_2fa_mutex);
  g_2fa_received = 0;
  g_2fa_code[0] = '\0';
  pthread_mutex_unlock(&g_2fa_mutex);

  // Clear login error
  g_login_error[0] = '\0';

  // Start login thread
  pthread_t login_thread;
  if (pthread_create(&login_thread, NULL, login_thread_func, NULL) != 0) {
    send_json_response(connfd, 500, "{\"error\":\"failed to start login\"}");
    return;
  }
  pthread_detach(login_thread);

  fprintf(stderr, "[.] /login: started login for %s\n", g_username);
  send_json_response(connfd, 202, "{\"message\":\"login started\"}");
}

static void handle_2fa(int connfd, const char *request) {
  pthread_mutex_lock(&status_mutex);
  login_status_t status = g_login_status;
  pthread_mutex_unlock(&status_mutex);

  if (status != STATUS_NEED_2FA) {
    send_json_response(connfd, 400, "{\"error\":\"2fa not required\"}");
    return;
  }

  // Parse JSON body
  const char *body = find_json_body(request);
  if (!body) {
    send_json_response(connfd, 400, "{\"error\":\"missing request body\"}");
    return;
  }

  cJSON *json = cJSON_Parse(body);
  if (!json) {
    send_json_response(connfd, 400, "{\"error\":\"invalid JSON\"}");
    return;
  }

  cJSON *code_obj = cJSON_GetObjectItemCaseSensitive(json, "code");
  if (!cJSON_IsString(code_obj) || strlen(code_obj->valuestring) != 6) {
    cJSON_Delete(json);
    send_json_response(connfd, 400, "{\"error\":\"6-digit code required\"}");
    return;
  }

  // Set 2FA code and signal waiting thread
  pthread_mutex_lock(&g_2fa_mutex);
  strncpy(g_2fa_code, code_obj->valuestring, 6);
  g_2fa_code[6] = '\0';
  g_2fa_received = 1;
  pthread_cond_signal(&g_2fa_cond);
  pthread_mutex_unlock(&g_2fa_mutex);

  cJSON_Delete(json);
  fprintf(stderr, "[.] /2fa: code received\n");
  send_json_response(connfd, 202, "{\"message\":\"2fa code received\"}");
}

static void handle_events(int connfd) {
  // Send SSE headers
  const char *headers = "HTTP/1.1 200 OK\r\n"
                        "Content-Type: text/event-stream\r\n"
                        "Cache-Control: no-cache\r\n"
                        "Connection: keep-alive\r\n"
                        "Access-Control-Allow-Origin: *\r\n\r\n";
  write(connfd, headers, strlen(headers));

  // Register client
  pthread_mutex_lock(&sse_mutex);
  if (sse_client_count < MAX_SSE_CLIENTS) {
    sse_clients[sse_client_count++] = connfd;
    fprintf(stderr, "[SSE] client connected (fd=%d, total=%d)\n", connfd,
            sse_client_count);
  } else {
    pthread_mutex_unlock(&sse_mutex);
    fprintf(stderr, "[SSE] max clients reached, rejecting connection\n");
    return;
  }
  pthread_mutex_unlock(&sse_mutex);

  // Send current status with consistent format
  pthread_mutex_lock(&status_mutex);
  login_status_t status = g_login_status;
  pthread_mutex_unlock(&status_mutex);

  char timestamp[32];
  time_t now = time(NULL);
  struct tm *tm_info = localtime(&now);
  strftime(timestamp, sizeof(timestamp), "%Y-%m-%dT%H:%M:%S", tm_info);

  char event_data[512];
  if (status == STATUS_LOGGED_IN && g_storefront_id && g_music_token) {
    snprintf(event_data, sizeof(event_data),
             "{\"type\":\"status_change\",\"status\":\"%s\","
             "\"storefront_id\":\"%s\",\"music_token\":\"%.14s...\","
             "\"timestamp\":\"%s\"}",
             get_status_string(status), g_storefront_id ? g_storefront_id : "",
             g_music_token ? g_music_token : "", timestamp);
  } else if (status == STATUS_LOGIN_FAILED && g_login_error[0] != '\0') {
    snprintf(event_data, sizeof(event_data),
             "{\"type\":\"status_change\",\"status\":\"%s\",\"error\":\"%s\","
             "\"timestamp\":\"%s\"}",
             get_status_string(status), g_login_error, timestamp);
  } else {
    snprintf(
        event_data, sizeof(event_data),
        "{\"type\":\"status_change\",\"status\":\"%s\",\"timestamp\":\"%s\"}",
        get_status_string(status), timestamp);
  }
  send_sse_event(connfd, event_data);
  fprintf(stderr, "[SSE] sent initial status to fd=%d: %s\n", connfd,
          event_data);

  // Keep connection alive until client disconnects
  while (1) {
    char buf[1];
    ssize_t ret = recv(connfd, buf, 1, MSG_PEEK | MSG_DONTWAIT);
    if (ret == 0) {
      // Client disconnected
      break;
    }
    if (ret < 0 && errno != EAGAIN && errno != EWOULDBLOCK) {
      break;
    }
    usleep(500000); // 500ms
  }

  // Remove client
  remove_sse_client(connfd);
  fprintf(stderr, "[SSE] client disconnected (fd=%d)\n", connfd);
}

static void handle_logout(int connfd) {
  pthread_mutex_lock(&status_mutex);
  login_status_t status = g_login_status;
  pthread_mutex_unlock(&status_mutex);

  if (status == STATUS_LOGGING_IN || status == STATUS_NEED_2FA) {
    send_json_response(connfd, 400, "{\"error\":\"login in progress\"}");
    return;
  }

  // Delete saved credential files
  char storefront_path[512], music_token_path[512];
  snprintf(storefront_path, sizeof(storefront_path), "%s/STOREFRONT_ID",
           args_info.base_dir_arg);
  snprintf(music_token_path, sizeof(music_token_path), "%s/MUSIC_TOKEN",
           args_info.base_dir_arg);

  if (file_exists(storefront_path)) {
    remove(storefront_path);
  }
  if (file_exists(music_token_path)) {
    remove(music_token_path);
  }

  // Clear in-memory credentials
  if (g_storefront_id) {
    free(g_storefront_id);
    g_storefront_id = NULL;
  }
  if (g_dev_token) {
    free(g_dev_token);
    g_dev_token = NULL;
  }
  if (g_music_token) {
    free(g_music_token);
    g_music_token = NULL;
  }

  // Reset login state
  g_username[0] = '\0';
  g_password[0] = '\0';
  g_login_error[0] = '\0';

  set_login_status(STATUS_NEED_LOGIN);

  fprintf(stderr, "[.] /logout: cleared login state\n");
  send_json_response(connfd, 200, "{\"message\":\"logged out\"}");
}

// ===== Main Account Handler =====

void handle_account(const int connfd) {
  char buffer[4096];
  ssize_t n = read(connfd, buffer, sizeof(buffer) - 1);
  if (n <= 0) {
    return;
  }
  buffer[n] = '\0';

  char method[16] = {0};
  char path[256] = {0};
  parse_http_request(buffer, method, path);

  fprintf(stderr, "[.] account API: %s %s\n", method, path);

  if (strcmp(method, "GET") == 0 && strcmp(path, "/info") == 0) {
    handle_info(connfd);
  } else if (strcmp(method, "POST") == 0 && strcmp(path, "/login") == 0) {
    handle_login(connfd, buffer);
  } else if (strcmp(method, "POST") == 0 && strcmp(path, "/2fa") == 0) {
    handle_2fa(connfd, buffer);
  } else if (strcmp(method, "POST") == 0 && strcmp(path, "/logout") == 0) {
    handle_logout(connfd);
  } else if (strcmp(method, "GET") == 0 && strcmp(path, "/events") == 0) {
    handle_events(connfd);
    return; // Don't close connection for SSE
  } else {
    send_json_response(connfd, 404, "{\"error\":\"not found\"}");
  }
}

// Worker thread for handling account connections
static void *account_worker_thread(void *arg) {
  thread_args_t *args = (thread_args_t *)arg;
  int connfd = args->connfd;
  free(args);

  handle_account(connfd);

  if (close(connfd) == -1) {
    perror("close");
  }

  return NULL;
}

static inline void *new_socket_account(void *args) {
  const int fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, IPPROTO_TCP);
  if (fd == -1) {
    perror("socket");
    return NULL;
  }
  const int optval = 1;
  setsockopt(fd, SOL_SOCKET, SO_REUSEPORT, &optval, sizeof(optval));

  static struct sockaddr_in serv_addr = {.sin_family = AF_INET};
  inet_pton(AF_INET, args_info.host_arg, &serv_addr.sin_addr);
  serv_addr.sin_port = htons(args_info.account_port_arg);
  if (bind(fd, (struct sockaddr *)&serv_addr, sizeof(serv_addr)) == -1) {
    perror("bind");
    return NULL;
  }

  if (listen(fd, 32) == -1) { // Increased backlog for concurrent connections
    perror("listen");
    return NULL;
  }

  fprintf(stderr,
          "[!] listening account info request on %s:%d (concurrent mode)\n",
          args_info.host_arg, args_info.account_port_arg);

  static struct sockaddr_in peer_addr;
  static socklen_t peer_addr_size = sizeof(peer_addr);
  while (1) {
    const int connfd = accept4(fd, (struct sockaddr *)&peer_addr,
                               &peer_addr_size, SOCK_CLOEXEC);
    if (connfd == -1) {
      if (errno == ENETDOWN || errno == EPROTO || errno == ENOPROTOOPT ||
          errno == EHOSTDOWN || errno == ENONET || errno == EHOSTUNREACH ||
          errno == EOPNOTSUPP || errno == ENETUNREACH)
        continue;
      perror("accept4");
    }

    // Create thread arguments
    thread_args_t *thread_args = malloc(sizeof(thread_args_t));
    if (thread_args == NULL) {
      perror("malloc thread args");
      close(connfd);
      continue;
    }
    thread_args->connfd = connfd;

    // Create worker thread for this connection
    pthread_t worker_thread;
    if (pthread_create(&worker_thread, NULL, account_worker_thread,
                       thread_args) != 0) {
      perror("pthread_create");
      free(thread_args);
      close(connfd);
      continue;
    }
    pthread_detach(worker_thread);
  }
}

char *get_account_storefront_id(struct shared_ptr reqCtx) {
  union std_string *region = malloc(sizeof(union std_string));
  struct shared_ptr urlbag = {.obj = 0x0, .ctrl_blk = 0x0};
  _ZNK17storeservicescore14RequestContext20storeFrontIdentifierERKNSt6__ndk110shared_ptrINS_6URLBagEEE(
      region, reqCtx.obj, &urlbag);
  const char *region_str = std_string_data(region);
  if (region_str) {
    char *result = strdup(region_str);
    free(region);
    return result;
  }
  return NULL;
}

void write_storefront_id(void) {
  FILE *fp = fopen(strcat_b(args_info.base_dir_arg, "/STOREFRONT_ID"), "w");
  printf("[+] StoreFront ID: %s\n", g_storefront_id);
  fprintf(fp, "%s", g_storefront_id);
  fclose(fp);
}

char *get_guid() {
  char *ret[2];
  _ZN17storeservicescore10DeviceGUID4guidEv(ret, GUID.obj);
  char *guid = _ZNK13mediaplatform4Data5bytesEv(ret[0]);
  return guid;
}

long long getCurrentTimeMillis() {
  struct timeval tv;
  gettimeofday(&tv, NULL);
  return tv.tv_sec * 1000LL + tv.tv_usec / 1000;
}

char *get_music_user_token(char *guid, char *authToken,
                           struct shared_ptr reqCtx) {
  uint8_t ptr[480];
  *(void **)(ptr) =
      &_ZTVNSt6__ndk120__shared_ptr_emplaceIN13mediaplatform11HTTPMessageENS_9allocatorIS2_EEEE +
      2;
  struct shared_ptr httpMessage = {.obj = ptr + 32, .ctrl_blk = ptr};
  union std_string url =
      new_std_string("https://play.itunes.apple.com/WebObjects/MZPlay.woa/wa/"
                     "createMusicToken");
  union std_string method = new_std_string("POST");
  _ZN13mediaplatform11HTTPMessageC2ENSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEES7_(
      httpMessage.obj, &url, &method);
  union std_string contentTypeHeader = new_std_string("Content-Type");
  union std_string contentTypeValue =
      new_std_string("application/json; charset=UTF-8");
  _ZN13mediaplatform11HTTPMessage9setHeaderERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEES9_(
      httpMessage.obj, &contentTypeHeader, &contentTypeValue);
  union std_string expectHeader = new_std_string("Expect");
  union std_string expectValue = new_std_string("");
  _ZN13mediaplatform11HTTPMessage9setHeaderERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEES9_(
      httpMessage.obj, &expectHeader, &expectValue);
  union std_string bundleIdHeader =
      new_std_string("X-Apple-Requesting-Bundle-Id");
  union std_string bundleIdValue = new_std_string("com.apple.android.music");
  _ZN13mediaplatform11HTTPMessage9setHeaderERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEES9_(
      httpMessage.obj, &bundleIdHeader, &bundleIdValue);
  union std_string bundleVersionHeader =
      new_std_string("X-Apple-Requesting-Bundle-Version");
  union std_string bundleVersionValue = new_std_string(
      "Music/4.9 Android/10 model/Samsung S9 build/7663313 (dt:66)");
  _ZN13mediaplatform11HTTPMessage9setHeaderERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEES9_(
      httpMessage.obj, &bundleVersionHeader, &bundleVersionValue);
  size_t body_size = 512;
  char *body = (char *)malloc(body_size);
  if (body == NULL) {
    return "";
  }

  snprintf(body, body_size,
           "{\"guid\":\"%s\",\"assertion\":\"%s\",\"tcc-acceptance-date\":\"%"
           "lld\"}",
           guid, authToken, getCurrentTimeMillis());

  _ZN13mediaplatform11HTTPMessage11setBodyDataEPcm(httpMessage.obj, body,
                                                   strlen(body));
  free(body);
  uint8_t urlRequest[512];
  _ZN17storeservicescore10URLRequestC2ERKNSt6__ndk110shared_ptrIN13mediaplatform11HTTPMessageEEERKNS2_INS_14RequestContextEEE(
      urlRequest, &httpMessage, &reqCtx);
  _ZN17storeservicescore10URLRequest3runEv(urlRequest);
  struct shared_ptr *err =
      _ZNK17storeservicescore10URLRequest5errorEv(urlRequest);
  if (err->obj != NULL) {
    return "";
  }
  struct shared_ptr *urlResp =
      _ZNK17storeservicescore10URLRequest8responseEv(urlRequest);
  struct shared_ptr *resp =
      _ZNK17storeservicescore11URLResponse18underlyingResponseEv(urlResp->obj);
  void *http_message_obj = resp->obj;
  void **data_ptr_location = (void **)((char *)http_message_obj + 48);
  void *data_ptr = *data_ptr_location;
  char *respBody = _ZNK13mediaplatform4Data5bytesEv(data_ptr);
  cJSON *json = cJSON_Parse(respBody);
  cJSON *token_obj = cJSON_GetObjectItemCaseSensitive(json, "music_token");
  char *token = cJSON_GetStringValue(token_obj);
  char *result = strdup(token);
  return result;
}

char *get_dev_token(struct shared_ptr reqCtx) {
  uint8_t ptr[480];
  *(void **)(ptr) =
      &_ZTVNSt6__ndk120__shared_ptr_emplaceIN13mediaplatform11HTTPMessageENS_9allocatorIS2_EEEE +
      2;
  struct shared_ptr httpMessage = {.obj = ptr + 32, .ctrl_blk = ptr};
  union std_string url =
      new_std_string("https://sf-api-token-service.itunes.apple.com/apiToken");
  union std_string method = new_std_string("GET");
  _ZN13mediaplatform11HTTPMessageC2ENSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEES7_(
      httpMessage.obj, &url, &method);
  uint8_t urlRequest[512];
  _ZN17storeservicescore10URLRequestC2ERKNSt6__ndk110shared_ptrIN13mediaplatform11HTTPMessageEEERKNS2_INS_14RequestContextEEE(
      urlRequest, &httpMessage, &reqCtx);
  union std_string clientIdName = new_std_string("clientId");
  union std_string clientIdValue = new_std_string("musicAndroid");
  _ZN17storeservicescore10URLRequest19setRequestParameterERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEES9_(
      urlRequest, &clientIdName, &clientIdValue);
  union std_string versionName = new_std_string("version");
  union std_string versionValue = new_std_string("1");
  _ZN17storeservicescore10URLRequest19setRequestParameterERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEES9_(
      urlRequest, &versionName, &versionValue);
  _ZN17storeservicescore10URLRequest3runEv(urlRequest);
  struct shared_ptr *err =
      _ZNK17storeservicescore10URLRequest5errorEv(urlRequest);
  if (err->obj != NULL) {
    return "";
  }
  struct shared_ptr *urlResp =
      _ZNK17storeservicescore10URLRequest8responseEv(urlRequest);
  struct shared_ptr *resp =
      _ZNK17storeservicescore11URLResponse18underlyingResponseEv(urlResp->obj);
  void *http_message_obj = resp->obj;
  void **data_ptr_location = (void **)((char *)http_message_obj + 48);
  void *data_ptr = *data_ptr_location;
  char *respBody = _ZNK13mediaplatform4Data5bytesEv(data_ptr);
  cJSON *json = cJSON_Parse(respBody);
  cJSON *token_obj = cJSON_GetObjectItemCaseSensitive(json, "token");
  char *token = cJSON_GetStringValue(token_obj);
  char *result = strdup(token);
  return result;
}

void write_music_token(void) {
  int token_file_available = 0;
  if (file_exists(strcat_b(args_info.base_dir_arg, "/MUSIC_TOKEN"))) {
    FILE *fp = fopen(strcat_b(args_info.base_dir_arg, "/MUSIC_TOKEN"), "r");
    if (NULL != fp) {
      fseek(fp, 0, SEEK_END);
      long size = ftell(fp);

      if (0 != size) {
        token_file_available = 1;
      }
    }
  }
  if (token_file_available) {
    char token[256];
    FILE *fp = fopen(strcat_b(args_info.base_dir_arg, "/MUSIC_TOKEN"), "r");
    fgets(token, sizeof(token), fp);
    printf("[+] Music-Token: %.14s...\n", token);
    return;
  }
  FILE *fp = fopen(strcat_b(args_info.base_dir_arg, "/MUSIC_TOKEN"), "w");
  printf("[+] Music-Token: %.14s...\n", g_music_token);
  fprintf(fp, "%s", g_music_token);
  fclose(fp);
}

int offline_available() {
  struct shared_ptr *fairplay = malloc(16);
  _ZN17storeservicescore14RequestContext8fairPlayEv(fairplay, reqCtx.obj);
  struct std_vector fairplay_status =
      _ZN17storeservicescore8FairPlay21getSubscriptionStatusEv(fairplay->obj);
  char *begin_ptr = (char *)fairplay_status.begin;
  char *second_item_ptr = begin_ptr + 16;
  int state = *(int *)((char *)second_item_ptr + 8);
  if (state == 2 || state == 3) { // kFPSubscriptionCanPlayContent,
                                  // kFPSubscriptionCanStreamAndPlayContent
    return 1;
  }
  return 0;
}

int main(int argc, char *argv[]) {
  cmdline_parser(argc, argv, &args_info);
  char *copy_that_needs_to_be_freed = NULL;
  split_string_safe(args_info.device_info_arg, "/", device_infos, 9,
                    &copy_that_needs_to_be_freed);

#ifndef MyRelease
  subhook_install(subhook_new(
      _ZN13mediaplatform26DebugLogEnabledForPriorityENS_11LogPriorityE,
      allDebug, SUBHOOK_64BIT_OFFSET));
  curl_hook = subhook_new(curl_easy_setopt, curl_easy_setopt_hook,
                          SUBHOOK_64BIT_OFFSET);
  subhook_install(curl_hook);
  subhook_install(subhook_new(__android_log_print, android_log_print_hook,
                              SUBHOOK_64BIT_OFFSET));
  subhook_install(subhook_new(__android_log_write, android_log_write_hook,
                              SUBHOOK_64BIT_OFFSET));
#endif

  init();
  reqCtx = init_ctx();

  if (args_info.login_given) {
    g_login_status = STATUS_LOGGING_IN;
    char *user = strtok(args_info.login_arg, ":");
    char *pass = strtok(NULL, ":");
    if (user) {
      strncpy(g_username, user, sizeof(g_username) - 1);
    }
    if (pass) {
      strncpy(g_password, pass, sizeof(g_password) - 1);
    }
    amUsername = g_username;
    amPassword = g_password;

    fprintf(stderr, "[+] logging in...\n");
    if (file_exists(strcat_b(args_info.base_dir_arg, "/STOREFRONT_ID"))) {
      remove(strcat_b(args_info.base_dir_arg, "/STOREFRONT_ID"));
    }
    if (file_exists(strcat_b(args_info.base_dir_arg, "/MUSIC_TOKEN"))) {
      remove(strcat_b(args_info.base_dir_arg, "/MUSIC_TOKEN"));
    }

    struct shared_ptr flow;
    _ZNSt6__ndk110shared_ptrIN17storeservicescore16AuthenticateFlowEE11make_sharedIJRNS0_INS1_14RequestContextEEEEEES3_DpOT_(
        &flow, &reqCtx);
    _ZN17storeservicescore16AuthenticateFlow3runEv(flow.obj);
    struct shared_ptr *resp =
        _ZNK17storeservicescore16AuthenticateFlow8responseEv(flow.obj);

    if (resp == NULL || resp->obj == NULL) {
      fprintf(stderr, "[!] login failed\n");
      g_login_status = STATUS_LOGIN_FAILED;
      return EXIT_FAILURE;
    }

    const int respType =
        _ZNK17storeservicescore20AuthenticateResponse12responseTypeEv(resp->obj);
    if (respType != 6) {
      const char *customer_msg = std_string_data(
          _ZNK17storeservicescore20AuthenticateResponse15customerMessageEv(resp->obj));
      if (customer_msg && *customer_msg) {
        fprintf(stderr, "[!] server message: %s\n", customer_msg);
      }

      struct shared_ptr *err = _ZNK17storeservicescore20AuthenticateResponse5errorEv(resp->obj);
      if (err != NULL && err->obj != NULL) {
        int code = _ZNK17storeservicescore19StoreErrorCondition9errorCodeEv(err->obj);
        const char *what = _ZNK17storeservicescore19StoreErrorCondition4whatEv(err->obj);
        fprintf(stderr, "[!] auth error: code=%d, message=%s\n", code, what ? what : "none");
      } else {
        fprintf(stderr, "[!] auth failed: response type %d\n", respType);
      }

      fprintf(stderr, "[!] login failed\n");
      g_login_status = STATUS_LOGIN_FAILED;
      return EXIT_FAILURE;
    }

    // Initialize services after successful login
    _ZN22SVPlaybackLeaseManagerC2ERKNSt6__ndk18functionIFvRKiEEERKNS1_IFvRKNS0_10shared_ptrIN17storeservicescore19StoreErrorConditionEEEEEE(
        leaseMgr, &endLeaseCallback, &pbErrCallback);
    uint8_t autom = 1;
    _ZN22SVPlaybackLeaseManager25refreshLeaseAutomaticallyERKb(leaseMgr, &autom);
    _ZN22SVPlaybackLeaseManager12requestLeaseERKb(leaseMgr, &autom);
    FHinstance = _ZN21SVFootHillSessionCtrl8instanceEv();

    offlineFlag = offline_available();
    if (offlineFlag) {
      fprintf(stderr, "[+] This account supports offline channel\n");
    }

    // Cache account info
    g_storefront_id = get_account_storefront_id(reqCtx);
    if (g_storefront_id == NULL) {
      fprintf(stderr, "[!] failed to get storefront ID\n");
      return EXIT_FAILURE;
    }
    g_dev_token = get_dev_token(reqCtx);
    if (g_dev_token == NULL) {
      fprintf(stderr, "[!] failed to get dev token\n");
      return EXIT_FAILURE;
    }
    g_music_token = get_music_user_token(get_guid(), g_dev_token, reqCtx);
    if (g_music_token == NULL) {
      fprintf(stderr, "[!] failed to get music token\n");
      return EXIT_FAILURE;
    }
    fprintf(stderr, "[+] account info cached successfully\n");

    write_storefront_id();
    write_music_token();
    fprintf(stderr, "[!] listening m3u8 request on %s:%d (concurrent mode)\n",
            args_info.host_arg, args_info.m3u8_port_arg);

    g_login_status = STATUS_LOGGED_IN;
  } else {
    // Check for saved credentials to determine initial login status
    char storefront_path[512], music_token_path[512];
    snprintf(storefront_path, sizeof(storefront_path), "%s/STOREFRONT_ID",
             args_info.base_dir_arg);
    snprintf(music_token_path, sizeof(music_token_path), "%s/MUSIC_TOKEN",
             args_info.base_dir_arg);

    if (file_exists(storefront_path) && file_exists(music_token_path)) {
      // Load saved credentials
      fprintf(stderr, "[+] Found saved credentials, loading...\n");

      // Read storefront ID
      FILE *fp = fopen(storefront_path, "r");
      if (fp) {
        char buf[64] = {0};
        fgets(buf, sizeof(buf), fp);
        fclose(fp);
        g_storefront_id = strdup(buf);
      }

      // Read music token
      fp = fopen(music_token_path, "r");
      if (fp) {
        char buf[512] = {0};
        fgets(buf, sizeof(buf), fp);
        fclose(fp);
        g_music_token = strdup(buf);
      }

      // Try to get dev token
      g_dev_token = get_dev_token(reqCtx);

      // Initialize services
      _ZN22SVPlaybackLeaseManagerC2ERKNSt6__ndk18functionIFvRKiEEERKNS1_IFvRKNS0_10shared_ptrIN17storeservicescore19StoreErrorConditionEEEEEE(
          leaseMgr, &endLeaseCallback, &pbErrCallback);
      uint8_t autom = 1;
      _ZN22SVPlaybackLeaseManager25refreshLeaseAutomaticallyERKb(leaseMgr,
                                                                 &autom);
      _ZN22SVPlaybackLeaseManager12requestLeaseERKb(leaseMgr, &autom);
      FHinstance = _ZN21SVFootHillSessionCtrl8instanceEv();

      offlineFlag = offline_available();
      if (offlineFlag) {
        printf("[+] This account supports offline channel\n");
      }

      g_login_status = STATUS_LOGGED_IN;
      fprintf(stderr, "[+] Logged in with saved credentials\n");
      fprintf(stderr, "[!] listening m3u8 request on %s:%d (concurrent mode)\n",
              args_info.host_arg, args_info.m3u8_port_arg);
    } else {
      // No saved credentials, wait for API login
      g_login_status = STATUS_NEED_LOGIN;
      fprintf(stderr, "[!] No saved credentials, waiting for login via API\n");
    }
  }

  // Start service threads
  pthread_t m3u8_thread;
  pthread_create(&m3u8_thread, NULL, &new_socket_m3u8, NULL);
  pthread_detach(m3u8_thread);

  pthread_t account_thread;
  pthread_create(&account_thread, NULL, &new_socket_account, NULL);
  pthread_detach(account_thread);

  return new_socket();
}
